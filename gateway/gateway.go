// Package gateway is a local HTTP endpoint that any Cashu wallet can use as
// its mint URL. A request to
//
//	http://127.0.0.1:3338/<mint-host>/<path>
//
// is sent to https://<mint-host>/<path> through the Nym mixnet, with the
// wallet's identifying headers removed. The mint sees a Nym exit gateway's
// IP and a fixed User-Agent, never the wallet's IP or browser fingerprint.
//
// The gateway does not read or change Cashu payloads. Blinding, signatures
// and proofs stay end to end between wallet and mint; TLS runs from this
// process to the mint, so the exit gateway relays only ciphertext.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apeirs/cashumix/cashu"
)

// Defaults.
const (
	DefaultListen          = "127.0.0.1:3338"
	DefaultMaxRequestBody  = 1 << 20  // 1 MiB: a swap of a few thousand proofs
	DefaultMaxResponseBody = 16 << 20 // 16 MiB
	DefaultTimeout         = 5 * time.Minute
)

// Request headers forwarded to the mint. Everything else is dropped:
// cookies, Origin, Referer, Accept-Language, client hints, X-Forwarded-For,
// Forwarded, Via, Authorization, and the wallet's own User-Agent.
var forwardRequest = []string{
	"Accept",
	"Content-Type",
	// NUT-21 / NUT-22 authentication. Only present if the wallet uses an
	// auth-gated mint, which then knows who you are by design.
	"Clear-Auth",
	"Blind-Auth",
}

// Extra request headers forwarded on a WebSocket upgrade (NUT-17 /v1/ws).
var forwardUpgrade = []string{
	"Sec-Websocket-Key",
	"Sec-Websocket-Version",
	"Sec-Websocket-Protocol",
	"Sec-Websocket-Extensions",
}

// Response headers passed back to the wallet. Set-Cookie, Server, Alt-Svc,
// Strict-Transport-Security and the rest are dropped.
var forwardResponse = []string{
	"Content-Type",
	"Cache-Control",
	"Retry-After",
	"Upgrade",
	"Connection",
	"Sec-Websocket-Accept",
	"Sec-Websocket-Protocol",
	"Sec-Websocket-Extensions",
}

// Gateway forwards wallet requests to mints over the mixnet.
type Gateway struct {
	// Transport carries upstream requests. It must dial through the mixnet
	// (mixnet.NewTransport with a mixnet.Dialer); the gateway never dials.
	Transport http.RoundTripper
	// Default mint (e.g. "mint.minibits.cash/Bitcoin") for requests whose
	// path does not start with a host, such as /v1/info. Empty: none.
	Default string
	// Only, if non-empty, lists the mint hosts that may be reached.
	Only map[string]bool
	// Ports allowed upstream (default {443}).
	Ports map[int]bool
	// AllowPrivate permits private, loopback and link-local IP literals as
	// mint hosts (tests only; an exit gateway would refuse them anyway).
	AllowPrivate bool
	// UserAgent sent to every mint (default cashu.UserAgent).
	UserAgent string
	// CORSOrigin for browser wallets: "" or "*" lets any web page use the
	// gateway; "-" sends no CORS headers; otherwise a comma-separated list of
	// origins (e.g. "https://wallet.cashu.me"). With a list, a request that
	// carries an Origin header outside it is refused with 403, WebSocket
	// upgrades included (browsers do not apply CORS to WebSockets). Requests
	// without an Origin header (CLI wallets) are not affected.
	CORSOrigin string
	// Hosts accepted in the Host header besides loopback (DNS-rebinding guard).
	Hosts []string
	// MaxRequestBody and MaxResponseBody bound bodies (0 = defaults).
	MaxRequestBody, MaxResponseBody int64
	// Timeout per request (0 = DefaultTimeout). WebSockets are exempt.
	Timeout time.Duration
	// Mode is shown on the status page ("mixnet via 127.0.0.1:1080", "direct").
	Mode string
	Log  func(format string, args ...any)

	once  sync.Once
	proxy *httputil.ReverseProxy

	total, failed, active atomic.Int64
	mu                    sync.Mutex
	perMint               map[string]int
}

func (g *Gateway) logf(format string, args ...any) {
	if g.Log != nil {
		g.Log(format, args...)
	}
}

// TargetError is a bad or forbidden mint in the request path.
type TargetError struct {
	Status int
	Msg    string
}

func (e *TargetError) Error() string { return e.Msg }

func badTarget(format string, args ...any) error {
	return &TargetError{Status: http.StatusBadRequest, Msg: fmt.Sprintf(format, args...)}
}

// Target maps a gateway request URL to the upstream mint URL:
//
//	/mint.example.com/v1/info           -> https://mint.example.com/v1/info
//	/mint.minibits.cash/Bitcoin/v1/keys -> https://mint.minibits.cash/Bitcoin/v1/keys
//	/https://mint.example.com/v1/info   -> https://mint.example.com/v1/info
//	/mint.example.com:3338/v1/info      -> https://mint.example.com:3338/v1/info (if port 3338 is allowed)
//	/v1/info                            -> the default mint, if set
//
// The query string is kept. The scheme is always https.
func (g *Gateway) Target(in *url.URL) (*url.URL, error) {
	p := strings.TrimPrefix(in.EscapedPath(), "/")
	lower := strings.ToLower(p)
	for _, pre := range []string{"https://", "https:/"} {
		if strings.HasPrefix(lower, pre) {
			p = p[len(pre):]
			break
		}
	}
	if strings.HasPrefix(lower, "http:/") {
		return nil, badTarget("cashumix only reaches https mints (plain http would be readable at the mixnet exit)")
	}
	first, rest, _ := strings.Cut(p, "/")
	if !looksLikeHost(first) {
		if g.Default == "" {
			return nil, badTarget("no mint in the path: use http://%s/<mint-host>/... (for example /testnut.cashu.space/v1/info), or start the gateway with --default", DefaultListen)
		}
		def, err := cashu.NormalizeMint(g.Default)
		if err != nil {
			return nil, err
		}
		first = def.Host
		rest = strings.TrimPrefix(def.EscapedPath(), "/")
		if rest != "" && p != "" {
			rest += "/"
		}
		rest += p
	}
	host, port, err := g.checkHost(first)
	if err != nil {
		return nil, err
	}
	hostport := host
	if port != 443 {
		hostport = net.JoinHostPort(host, strconv.Itoa(port))
	}
	out := &url.URL{Scheme: "https", Host: hostport, RawQuery: in.RawQuery}
	if err := setPath(out, "/"+rest); err != nil {
		return nil, badTarget("bad path: %v", err)
	}
	return out, nil
}

func setPath(u *url.URL, escaped string) error {
	dec, err := url.PathUnescape(escaped)
	if err != nil {
		return err
	}
	u.Path = dec
	if escaped != dec {
		u.RawPath = escaped
	}
	return nil
}

// looksLikeHost reports whether a path segment is meant as a host: it has a
// dot or a port, or is a bracketed IPv6 literal. "v1" is not a host.
func looksLikeHost(s string) bool {
	return strings.Contains(s, ".") || strings.Contains(s, ":") || strings.HasPrefix(s, "[")
}

func (g *Gateway) checkHost(hp string) (string, int, error) {
	u, err := url.Parse("https://" + hp)
	if err != nil || u.Host != hp || u.User != nil {
		return "", 0, badTarget("bad mint host %q", hp)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	port := 443
	if ps := u.Port(); ps != "" {
		port, err = strconv.Atoi(ps)
		if err != nil || port <= 0 || port > 65535 {
			return "", 0, badTarget("bad port in %q", hp)
		}
	}
	ports := g.Ports
	if len(ports) == 0 {
		ports = map[int]bool{443: true}
	}
	if !ports[port] {
		return "", 0, &TargetError{Status: http.StatusForbidden, Msg: fmt.Sprintf("port %d not allowed (start the gateway with --allow-port %d)", port, port)}
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if !g.AllowPrivate && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()) {
			return "", 0, &TargetError{Status: http.StatusForbidden, Msg: fmt.Sprintf("%s is a private address", host)}
		}
		host = ip.String()
	} else if err := validHostname(host); err != nil {
		return "", 0, err
	}
	if strings.HasSuffix(host, ".onion") {
		return "", 0, badTarget("%s is a Tor onion service; Nym exit gateways cannot reach .onion hosts", host)
	}
	if len(g.Only) > 0 && !g.Only[host] && !g.Only[net.JoinHostPort(host, strconv.Itoa(port))] {
		return "", 0, &TargetError{Status: http.StatusForbidden, Msg: fmt.Sprintf("mint %s is not in the allowlist (--only)", host)}
	}
	return host, port, nil
}

func validHostname(h string) error {
	if len(h) == 0 || len(h) > 253 || !strings.Contains(h, ".") {
		return badTarget("bad mint host %q", h)
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return badTarget("bad mint host %q", h)
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return badTarget("bad mint host %q", h)
			}
		}
	}
	return nil
}

func (g *Gateway) hostOK(h string) bool {
	host := h
	if hh, _, err := net.SplitHostPort(h); err == nil {
		host = hh
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsLoopback() {
		return true
	}
	for _, a := range g.Hosts {
		if strings.EqualFold(a, host) {
			return true
		}
	}
	return false
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

type ctxKey struct{}

type reqInfo struct {
	target *url.URL
	origin string // the wallet's Origin header (never sent to the mint)
	start  time.Time
	upMsg  string
}

func (g *Gateway) init() {
	g.once.Do(func() {
		g.proxy = &httputil.ReverseProxy{
			Rewrite:        g.rewrite,
			Transport:      g.Transport,
			ModifyResponse: g.modifyResponse,
			ErrorHandler:   g.proxyError,
			ErrorLog:       log.New(io.Discard, "", 0), // failures go to the access log
			FlushInterval:  -1,
		}
		g.perMint = map[string]int{}
	})
}

func (g *Gateway) rewrite(pr *httputil.ProxyRequest) {
	ri := pr.In.Context().Value(ctxKey{}).(*reqInfo)
	out := pr.Out
	out.URL = ri.target
	out.Host = ri.target.Host
	h := http.Header{}
	copyHeaders(h, pr.In.Header, forwardRequest)
	if isUpgrade(pr.In) {
		copyHeaders(h, pr.In.Header, forwardUpgrade)
		h.Set("Connection", "Upgrade")
		h.Set("Upgrade", "websocket")
	}
	ua := g.UserAgent
	if ua == "" {
		ua = cashu.UserAgent
	}
	h.Set("User-Agent", ua)
	out.Header = h
}

func copyHeaders(dst, src http.Header, names []string) {
	for _, n := range names {
		if vs := src.Values(n); len(vs) > 0 {
			dst[http.CanonicalHeaderKey(n)] = append([]string(nil), vs...)
		}
	}
}

func (g *Gateway) maxResponse() int64 {
	if g.MaxResponseBody > 0 {
		return g.MaxResponseBody
	}
	return DefaultMaxResponseBody
}

var errTooLarge = errors.New("mint response too large")

func (g *Gateway) modifyResponse(resp *http.Response) error {
	ri, _ := resp.Request.Context().Value(ctxKey{}).(*reqInfo)
	if ri != nil && resp.StatusCode >= 500 {
		ri.upMsg = "mint answered " + resp.Status
	}
	h := http.Header{}
	copyHeaders(h, resp.Header, forwardResponse)
	resp.Header = h
	origin := ""
	if ri != nil {
		origin = ri.origin
	}
	g.cors(h, origin)
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return nil
	}
	if resp.ContentLength > g.maxResponse() {
		resp.Body.Close()
		return errTooLarge
	}
	resp.Body = &limitBody{rc: resp.Body, left: g.maxResponse()}
	return nil
}

// limitBody fails the copy once more than left bytes arrive, so a hostile
// or broken mint cannot stream an unbounded body into a wallet.
type limitBody struct {
	rc   io.ReadCloser
	left int64
}

func (l *limitBody) Read(p []byte) (int, error) {
	if l.left <= 0 {
		var one [1]byte
		if n, _ := l.rc.Read(one[:]); n > 0 {
			return 0, errTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.rc.Read(p)
	l.left -= int64(n)
	return n, err
}

func (l *limitBody) Close() error { return l.rc.Close() }

// origins returns the CORS allowlist, or nil when any origin may use the
// gateway ("" or "*") or CORS is off ("-").
func (g *Gateway) origins() []string {
	o := strings.TrimSpace(g.CORSOrigin)
	if o == "" || o == "*" || o == "-" {
		return nil
	}
	var list []string
	for _, x := range strings.Split(o, ",") {
		if x = strings.TrimRight(strings.TrimSpace(x), "/"); x != "" {
			list = append(list, x)
		}
	}
	return list
}

// originOK reports whether a request with this Origin header may use the
// gateway. No Origin (a CLI wallet) is always fine.
func (g *Gateway) originOK(origin string) bool {
	list := g.origins()
	if origin == "" || list == nil {
		return true
	}
	for _, o := range list {
		if strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

// cors sets Access-Control-Allow-Origin for a request from origin.
func (g *Gateway) cors(h http.Header, origin string) {
	o := strings.TrimSpace(g.CORSOrigin)
	switch {
	case o == "-":
		return
	case o == "" || o == "*":
		h.Set("Access-Control-Allow-Origin", "*")
		return
	}
	h.Add("Vary", "Origin")
	if origin != "" && g.originOK(origin) {
		h.Set("Access-Control-Allow-Origin", origin)
	}
}

// writeError answers in the NUT-00 error format, which wallets display.
func (g *Gateway) writeError(w http.ResponseWriter, r *http.Request, status int, detail string) {
	g.cors(w.Header(), r.Header.Get("Origin"))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"detail": "cashumix: " + detail, "code": 0})
}

func (g *Gateway) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	ri := r.Context().Value(ctxKey{}).(*reqInfo)
	status := http.StatusBadGateway
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe):
		status = http.StatusRequestEntityTooLarge
		err = fmt.Errorf("request body larger than %d bytes", mbe.Limit)
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		status = http.StatusGatewayTimeout
	}
	ri.upMsg = err.Error()
	g.writeError(w, r, status, fmt.Sprintf("%s %s over the mixnet: %v", r.Method, ri.target.Host, err))
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// statusWriter records the status for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
	n      int64
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = 200
	}
	n, err := s.ResponseWriter.Write(b)
	s.n += int64(n)
	return n, err
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.init()
	if !g.hostOK(r.Host) {
		http.Error(w, "cashumix: Host header not allowed (DNS-rebinding guard); see --host", http.StatusForbidden)
		return
	}
	origin := r.Header.Get("Origin")
	if r.URL.Path == "/.cashumix" || (r.URL.Path == "/" && r.Method == http.MethodGet && g.Default == "") {
		// Local pages for the user and local tools only. They carry no CORS
		// headers, and a web page may not load them at all: the status page
		// lists the mints you use.
		if origin != "" {
			http.Error(w, "cashumix: the status page is not available to web pages", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/.cashumix" {
			g.serveStatus(w)
		} else {
			g.serveHelp(w)
		}
		return
	}
	if !g.originOK(origin) {
		g.logf("%s %s -> 403 refused: Origin %q not in --cors", r.Method, r.URL.EscapedPath(), origin)
		http.Error(w, "cashumix: Origin not allowed; see --cors", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodOptions {
		// CORS preflight is answered here: it never crosses the mixnet.
		g.cors(w.Header(), origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Clear-Auth, Blind-Auth")
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		g.writeError(w, r, http.StatusMethodNotAllowed, "only GET and POST are used by Cashu mints")
		return
	}
	target, err := g.Target(r.URL)
	if err != nil {
		status := http.StatusBadRequest
		var te *TargetError
		if errors.As(err, &te) {
			status = te.Status
		}
		g.logf("%s %s -> %d refused: %v", r.Method, r.URL.EscapedPath(), status, err)
		g.writeError(w, r, status, err.Error())
		return
	}
	maxReq := g.MaxRequestBody
	if maxReq <= 0 {
		maxReq = DefaultMaxRequestBody
	}
	if r.ContentLength > maxReq {
		g.writeError(w, r, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body larger than %d bytes", maxReq))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxReq)

	ri := &reqInfo{target: target, origin: origin, start: time.Now()}
	ctx := context.WithValue(r.Context(), ctxKey{}, ri)
	upgrade := isUpgrade(r)
	if !upgrade {
		t := g.Timeout
		if t <= 0 {
			t = DefaultTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}
	g.total.Add(1)
	g.active.Add(1)
	defer g.active.Add(-1)
	g.mu.Lock()
	g.perMint[target.Host]++
	g.mu.Unlock()

	sw := &statusWriter{ResponseWriter: w}
	defer func() {
		p := recover()
		if upgrade && sw.status == 0 && p == nil && ri.upMsg == "" {
			sw.status = http.StatusSwitchingProtocols
		}
		if p != nil && ri.upMsg == "" {
			ri.upMsg = "upstream response aborted (too large or connection lost)"
		}
		if sw.status >= 500 || ri.upMsg != "" {
			g.failed.Add(1)
		}
		kind, msg := "", ""
		if upgrade {
			kind = " (websocket)"
		}
		if ri.upMsg != "" {
			msg = " error: " + ri.upMsg
		}
		g.logf("%s %s%s -> %d, %d bytes, %.2fs%s%s", r.Method, target.Host, target.EscapedPath(), sw.status, sw.n, time.Since(ri.start).Seconds(), kind, msg)
		if p != nil {
			panic(p)
		}
	}()
	g.proxy.ServeHTTP(sw, r.WithContext(ctx))
}

func (g *Gateway) serveHelp(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, `cashumix gateway: Cashu mint traffic through the Nym mixnet (%s)

Set your wallet's mint URL to this gateway followed by the mint's address:

    https://testnut.cashu.space          ->  http://%s/testnut.cashu.space
    https://mint.minibits.cash/Bitcoin   ->  http://%s/mint.minibits.cash/Bitcoin

The mint then sees a Nym exit gateway's IP, not yours. Status: /.cashumix
`, g.Mode, DefaultListen, DefaultListen)
}

func (g *Gateway) serveStatus(w http.ResponseWriter) {
	g.mu.Lock()
	mints := make([]string, 0, len(g.perMint))
	for m := range g.perMint {
		mints = append(mints, m)
	}
	sort.Strings(mints)
	per := map[string]int{}
	for _, m := range mints {
		per[m] = g.perMint[m]
	}
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"mode": g.Mode, "requests": g.total.Load(), "failed": g.failed.Load(), "active": g.active.Load(),
		"per_mint": per, "default": g.Default, "pid": os.Getpid(),
	})
}
