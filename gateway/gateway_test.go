package gateway_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/apeirs/cashumix/cashu"
	"github.com/apeirs/cashumix/gateway"
	"github.com/apeirs/cashumix/internal/fakes"
	"github.com/apeirs/cashumix/mixnet"
)

// world is a fake mixnet: SOCKS5 proxy -> {fake mint "mint.test", DoH at 1.1.1.1}.
type world struct {
	ca    *fakes.CA
	socks *fakes.SOCKS
	mint  *fakes.Mint
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ca := fakes.NewCA()
	mint := fakes.StartMint(ca, "mint.test", "other.test")
	doh := fakes.DoH(ca, "1.1.1.1", map[string][]string{"mint.test": {"10.0.0.7"}})
	s := fakes.NewSOCKS(map[string]string{
		"mint.test:443":  mint.Addr,
		"mint.test:3338": mint.Addr,
		"other.test:443": mint.Addr,
		"evil.test:443":  mint.Addr, // the mint's certificate is not valid for evil.test
		"1.1.1.1:443":    doh,
		"10.0.0.7:443":   mint.Addr,
	})
	t.Cleanup(func() { s.Close(); mint.Close() })
	return &world{ca: ca, socks: s, mint: mint}
}

func (w *world) dialer(dns string) *mixnet.Dialer {
	return &mixnet.Dialer{Proxy: w.socks.Addr, DNS: dns, RootCAs: w.ca.Pool, Resolvers: []string{"1.1.1.1"}, Timeout: 10 * time.Second}
}

func (w *world) gateway(t *testing.T, edit func(*gateway.Gateway, *mixnet.TransportOptions)) (*gateway.Gateway, string) {
	t.Helper()
	g := &gateway.Gateway{Mode: "test"}
	opts := mixnet.TransportOptions{RootCAs: w.ca.Pool}
	if edit != nil {
		edit(g, &opts)
	}
	if g.Transport == nil {
		g.Transport = mixnet.NewTransport(w.dialer(mixnet.DNSExit).DialContext, opts)
	}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return g, srv.URL
}

func get(t *testing.T, u string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return do(t, req)
}

func do(t *testing.T, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 20 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestTarget(t *testing.T) {
	g := &gateway.Gateway{Ports: map[int]bool{443: true, 3338: true}}
	for in, want := range map[string]string{
		"/mint.example.com/v1/info":           "https://mint.example.com/v1/info",
		"/mint.minibits.cash/Bitcoin/v1/keys": "https://mint.minibits.cash/Bitcoin/v1/keys",
		"/https://mint.example.com/v1/info":   "https://mint.example.com/v1/info",
		"/https:/mint.example.com/v1/info":    "https://mint.example.com/v1/info",
		"/Mint.Example.COM./v1/keys/00ad268c": "https://mint.example.com/v1/keys/00ad268c",
		"/mint.example.com:3338/v1/info":      "https://mint.example.com:3338/v1/info",
		"/mint.example.com:443/v1/info":       "https://mint.example.com/v1/info",
		"/mint.example.com/v1/melt/quote?x=1": "https://mint.example.com/v1/melt/quote?x=1",
		"/mint.example.com":                   "https://mint.example.com/",
		"/203.0.113.9/v1/info":                "https://203.0.113.9/v1/info",
		"/mint.example.com/v1/a%2Fb":          "https://mint.example.com/v1/a%2Fb",
	} {
		u, _ := url.Parse("http://127.0.0.1:3338" + in)
		got, err := g.Target(u)
		if err != nil || got.String() != want {
			t.Errorf("Target(%s) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{
		"/", "/v1/info", // no default mint
		"/http://mint.example.com/v1/info",
		"/mint.example.com:8080/v1/info",
		"/127.0.0.1/v1/info", "/10.1.2.3/v1/info", "/[::1]/v1/info", "/169.254.1.1/v1/info",
		"/abcdefghijklmnop.onion/v1/info",
		"/bad_host!.com/v1/info", "/-x.example.com/v1/info", "/user@mint.example.com/v1/info",
		"/mint.example.com:99999/v1/info",
	} {
		u, _ := url.Parse("http://127.0.0.1:3338" + in)
		if got, err := g.Target(u); err == nil {
			t.Errorf("Target(%s) = %v, want an error", in, got)
		}
	}
	d := &gateway.Gateway{Default: "mint.minibits.cash/Bitcoin"}
	for in, want := range map[string]string{
		"/v1/info":                "https://mint.minibits.cash/Bitcoin/v1/info",
		"/":                       "https://mint.minibits.cash/Bitcoin",
		"/testnut.cashu.space/v1": "https://testnut.cashu.space/v1",
	} {
		u, _ := url.Parse("http://127.0.0.1:3338" + in)
		if got, err := d.Target(u); err != nil || got.String() != want {
			t.Errorf("default: Target(%s) = %v, %v; want %s", in, got, err, want)
		}
	}
	only := &gateway.Gateway{Only: map[string]bool{"testnut.cashu.space": true}}
	u, _ := url.Parse("http://127.0.0.1:3338/mint.example.com/v1/info")
	if _, err := only.Target(u); err == nil {
		t.Error("allowlist not enforced")
	}
}

func TestForwardsThroughSOCKSAndStripsHeaders(t *testing.T) {
	w := newWorld(t)
	_, base := w.gateway(t, nil)
	resp, body := get(t, base+"/mint.test/v1/info", map[string]string{
		"User-Agent":      "Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0 MyWallet/1.2.3",
		"Accept":          "application/json",
		"Accept-Language": "da-DK,da;q=0.9",
		"Cookie":          "session=abc",
		"Origin":          "https://wallet.example",
		"Referer":         "https://wallet.example/receive",
		"X-Forwarded-For": "192.0.2.55",
		"X-Real-Ip":       "192.0.2.55",
		"Forwarded":       "for=192.0.2.55",
		"Via":             "1.1 corp-proxy",
		"Authorization":   "Bearer secret",
		"Sec-Ch-Ua":       `"Chromium";v="140"`,
		"Dnt":             "1",
	})
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"Fake mint"`) {
		t.Fatalf("HTTP %d %s", resp.StatusCode, body)
	}
	got := w.mint.Last()
	if got.Path != "/v1/info" || got.SNI != "mint.test" {
		t.Fatalf("mint saw path %q SNI %q", got.Path, got.SNI)
	}
	if ua := got.Header.Get("User-Agent"); ua != cashu.UserAgent {
		t.Errorf("User-Agent = %q, want %q", ua, cashu.UserAgent)
	}
	if got.Header.Get("Accept") != "application/json" {
		t.Errorf("Accept not forwarded: %v", got.Header)
	}
	for _, h := range []string{"Accept-Language", "Cookie", "Origin", "Referer", "X-Forwarded-For", "X-Real-Ip",
		"Forwarded", "Via", "Authorization", "Sec-Ch-Ua", "Dnt", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if v := got.Header.Get(h); v != "" {
			t.Errorf("mint received %s: %q", h, v)
		}
	}
	seen := w.socks.Seen()
	if len(seen) != 1 || seen[0].Host != "mint.test" || seen[0].Port != 443 || seen[0].ATYP != 3 {
		t.Fatalf("SOCKS saw %v (want one CONNECT by name to mint.test:443)", seen)
	}
}

func TestPathPrefixPostAndMintErrors(t *testing.T) {
	w := newWorld(t)
	_, base := w.gateway(t, nil)
	_, body := get(t, base+"/mint.test/Bitcoin/v1/info", nil)
	if !strings.Contains(string(body), "Fake mint (Bitcoin)") || w.mint.Last().Path != "/Bitcoin/v1/info" {
		t.Fatalf("prefix: %s, mint saw %s", body, w.mint.Last().Path)
	}

	y1, _ := cashu.Y("secret one")
	y2, _ := cashu.Y("secret two")
	w.mint.Spent[y2] = true
	reqBody := `{"Ys":["` + y1 + `","` + y2 + `"]}`
	req, _ := http.NewRequest("POST", base+"/mint.test/v1/checkstate", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	resp, body := do(t, req)
	var out struct{ States []cashu.ProofState }
	if resp.StatusCode != 200 || json.Unmarshal(body, &out) != nil || len(out.States) != 2 || out.States[1].State != "SPENT" {
		t.Fatalf("checkstate: HTTP %d %s", resp.StatusCode, body)
	}
	last := w.mint.Last()
	if string(last.Body) != reqBody || last.Header.Get("Content-Type") != "application/json" || last.Method != "POST" {
		t.Fatalf("mint got %s %q %v", last.Method, last.Body, last.Header)
	}

	// Mint errors pass through unchanged, so wallets show the mint's message.
	resp, body = get(t, base+"/mint.test/v1/fail", nil)
	if resp.StatusCode != 400 || !strings.Contains(string(body), "Token already spent.") || !strings.Contains(string(body), "11001") {
		t.Fatalf("mint error: HTTP %d %s", resp.StatusCode, body)
	}
}

func TestResponseHeadersAndCORS(t *testing.T) {
	w := newWorld(t)
	_, base := w.gateway(t, nil)
	resp, _ := get(t, base+"/mint.test/v1/cookie", nil)
	for _, h := range []string{"Set-Cookie", "Server", "Alt-Svc"} {
		if v := resp.Header.Get(h); v != "" {
			t.Errorf("wallet received %s: %q", h, v)
		}
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" || resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers: %v", resp.Header)
	}

	n := len(w.mint.Requests())
	req, _ := http.NewRequest("OPTIONS", base+"/mint.test/v1/swap", nil)
	req.Header.Set("Origin", "https://wallet.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	resp, _ = do(t, req)
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Private-Network") != "true" ||
		!strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "POST") {
		t.Fatalf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
	if len(w.mint.Requests()) != n || len(w.socks.Seen()) != 1 {
		t.Fatal("preflight crossed the mixnet")
	}
}

func TestGuards(t *testing.T) {
	w := newWorld(t)
	_, base := w.gateway(t, func(g *gateway.Gateway, _ *mixnet.TransportOptions) {
		g.MaxRequestBody = 1000
		g.MaxResponseBody = 10000
		g.Only = map[string]bool{"mint.test": true, "evil.test": true}
	})

	// DNS rebinding: a web page that rebinds its own name to 127.0.0.1.
	req, _ := http.NewRequest("GET", base+"/mint.test/v1/info", nil)
	req.Host = "attacker.example:3338"
	if resp, _ := do(t, req); resp.StatusCode != 403 {
		t.Errorf("foreign Host header: HTTP %d", resp.StatusCode)
	}
	if resp, _ := get(t, base+"/other.test/v1/info", nil); resp.StatusCode != 403 {
		t.Errorf("allowlist: HTTP %d", resp.StatusCode)
	}
	if resp, _ := get(t, base+"/mint.test:3338/v1/info", nil); resp.StatusCode != 403 {
		t.Errorf("port 3338 without --allow-port: HTTP %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("PUT", base+"/mint.test/v1/info", nil)
	if resp, _ := do(t, req); resp.StatusCode != 405 {
		t.Errorf("PUT: HTTP %d", resp.StatusCode)
	}
	big := strings.Repeat("a", 2000)
	req, _ = http.NewRequest("POST", base+"/mint.test/v1/swap", strings.NewReader(big))
	if resp, _ := do(t, req); resp.StatusCode != 413 {
		t.Errorf("large body: HTTP %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", base+"/mint.test/v1/swap", io.MultiReader(strings.NewReader(big))) // chunked
	req.ContentLength = -1
	if resp, body := do(t, req); resp.StatusCode != 413 {
		t.Errorf("large chunked body: HTTP %d %s", resp.StatusCode, body)
	}
	if resp, body := get(t, base+"/mint.test/v1/big?n=20000", nil); resp.StatusCode != 502 || !strings.Contains(string(body), "too large") {
		t.Errorf("large response: HTTP %d %s", resp.StatusCode, body)
	}
	c := &http.Client{Timeout: 10 * time.Second}
	if resp, err := c.Get(base + "/mint.test/v1/big?n=20000&chunked=1"); err == nil {
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err == nil && len(b) > 10000 {
			t.Errorf("large chunked response passed: %d bytes", len(b))
		}
	}
	if resp, body := get(t, base+"/mint.test/v1/big?n=5000", nil); resp.StatusCode != 200 || len(body) != 5000 {
		t.Errorf("small response: HTTP %d, %d bytes", resp.StatusCode, len(body))
	}
	for _, s := range w.socks.Seen() {
		if s.Host != "mint.test" && s.Host != "evil.test" {
			t.Errorf("refused request still reached SOCKS: %v", s)
		}
	}
	// TLS is verified end to end: a wrong certificate behind the exit fails.
	if resp, body := get(t, base+"/evil.test/v1/info", nil); resp.StatusCode != 502 || !strings.Contains(string(body), "certificate") {
		t.Errorf("bad certificate: HTTP %d %s", resp.StatusCode, body)
	}
}

func TestFreshConnections(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		w := newWorld(t)
		_, base := w.gateway(t, func(_ *gateway.Gateway, o *mixnet.TransportOptions) { o.Fresh = fresh })
		for i := 0; i < 3; i++ {
			if resp, _ := get(t, base+"/mint.test/v1/keysets", nil); resp.StatusCode != 200 {
				t.Fatalf("HTTP %d", resp.StatusCode)
			}
		}
		want := int64(1)
		if fresh {
			want = 3
		}
		if got := w.mint.Conns.Load(); got != want || int64(len(w.socks.Seen())) != want {
			t.Errorf("fresh=%v: %d mint connections, %d SOCKS streams; want %d", fresh, got, len(w.socks.Seen()), want)
		}
	}
}

func TestNoClearnetFallback(t *testing.T) {
	w := newWorld(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	_, base := w.gateway(t, func(g *gateway.Gateway, o *mixnet.TransportOptions) {
		g.Transport = mixnet.NewTransport((&mixnet.Dialer{Proxy: dead, DNS: mixnet.DNSExit}).DialContext, *o)
	})
	resp, body := get(t, base+"/mint.test/v1/info", nil)
	if resp.StatusCode != 502 || !strings.Contains(string(body), "refusing to fall back") {
		t.Fatalf("HTTP %d %s", resp.StatusCode, body)
	}
	if len(w.mint.Requests()) != 0 {
		t.Fatal("the mint was reached without the mixnet")
	}
}

func TestDoHOverMixnetAndPorts(t *testing.T) {
	w := newWorld(t)
	_, base := w.gateway(t, func(g *gateway.Gateway, o *mixnet.TransportOptions) {
		g.Ports = map[int]bool{443: true, 3338: true}
		g.Transport = mixnet.NewTransport(w.dialer(mixnet.DNSDoH).DialContext, *o)
	})
	if resp, body := get(t, base+"/mint.test/v1/info", nil); resp.StatusCode != 200 {
		t.Fatalf("HTTP %d %s", resp.StatusCode, body)
	}
	seen := w.socks.Seen()
	if len(seen) != 2 || seen[0].Host != "1.1.1.1" || seen[1].Host != "10.0.0.7" || seen[1].ATYP != 1 {
		t.Fatalf("SOCKS saw %v (want DoH at 1.1.1.1, then the mint by IPv4)", seen)
	}
	if w.mint.Last().SNI != "mint.test" {
		t.Fatalf("SNI %q", w.mint.Last().SNI)
	}
	_, base = w.gateway(t, func(g *gateway.Gateway, _ *mixnet.TransportOptions) { g.Ports = map[int]bool{3338: true} })
	if resp, _ := get(t, base+"/mint.test:3338/v1/info", nil); resp.StatusCode != 200 {
		t.Fatalf("port 3338: HTTP %d", resp.StatusCode)
	}
}

func TestDefaultMintTimeoutAndPages(t *testing.T) {
	w := newWorld(t)
	g, base := w.gateway(t, func(g *gateway.Gateway, _ *mixnet.TransportOptions) {
		g.Default = "mint.test/Bitcoin"
		g.Timeout = 300 * time.Millisecond
	})
	if _, body := get(t, base+"/v1/info", nil); !strings.Contains(string(body), "Fake mint (Bitcoin)") {
		t.Fatalf("default mint: %s", body)
	}
	if resp, body := get(t, base+"/v1/slow?d=2s", nil); resp.StatusCode != 504 {
		t.Fatalf("timeout: HTTP %d %s", resp.StatusCode, body)
	}
	_, body := get(t, base+"/.cashumix", nil)
	var st struct {
		Requests, Failed int
		PerMint          map[string]int `json:"per_mint"`
	}
	if json.Unmarshal(body, &st) != nil || st.Requests != 2 || st.Failed != 1 || st.PerMint["mint.test"] != 2 {
		t.Fatalf("status: %s", body)
	}
	g.Default = ""
	if _, body := get(t, base+"/", nil); !strings.Contains(string(body), "http://127.0.0.1:3338/testnut.cashu.space") {
		t.Fatalf("help: %s", body)
	}
}

func TestWebSocketPassthrough(t *testing.T) {
	w := newWorld(t)
	_, base := w.gateway(t, nil)
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	io.WriteString(conn, "GET /mint.test/v1/ws HTTP/1.1\r\nHost: 127.0.0.1:3338\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nOrigin: https://wallet.example\r\nUser-Agent: MyWallet\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 || resp.Header.Get("Sec-Websocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" || resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("upgrade: %d %v", resp.StatusCode, resp.Header)
	}
	io.WriteString(conn, "ping over the mixnet")
	buf := make([]byte, 64)
	n, _ := io.ReadAtLeast(br, buf, len("ping over the mixnet"))
	if string(buf[:n]) != "ping over the mixnet" {
		t.Fatalf("echo %q", buf[:n])
	}
	h := w.mint.Last().Header
	if h.Get("Origin") != "" || h.Get("User-Agent") != cashu.UserAgent || h.Get("Sec-Websocket-Key") == "" {
		t.Fatalf("upgrade headers at the mint: %v", h)
	}
}

// The mint is a real, directly reachable TLS server on localhost. With the
// mixnet client down, nothing may reach it, in either DNS mode and at either
// layer (gateway or CLI client): a direct dial would land on it and be seen.
func TestNoFallbackEvenWhenDirectWouldWork(t *testing.T) {
	ca := fakes.NewCA()
	mint := fakes.StartMint(ca, "localhost", "127.0.0.1")
	defer mint.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	_, port, _ := net.SplitHostPort(mint.Addr)
	var p int
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	for _, mode := range []string{mixnet.DNSDoH, mixnet.DNSExit} {
		tr := mixnet.NewTransport((&mixnet.Dialer{Proxy: dead, DNS: mode, RootCAs: ca.Pool, Timeout: time.Second}).DialContext, mixnet.TransportOptions{RootCAs: ca.Pool})
		g := &gateway.Gateway{Mode: "test", AllowPrivate: true, Ports: map[int]bool{443: true, p: true}, Transport: tr}
		srv := httptest.NewServer(g)
		// The gateway takes dotted names or IP literals only, so the
		// literal here; the CLI below uses the name.
		resp, body := get(t, srv.URL+"/127.0.0.1:"+port+"/v1/info", nil)
		if resp.StatusCode != 502 || !strings.Contains(string(body), "refusing to fall back") {
			t.Errorf("dns=%s: want 502, got %d %s", mode, resp.StatusCode, body)
		}
		srv.Close()
		cl := &cashu.Client{HTTP: &http.Client{Timeout: 3 * time.Second, Transport: tr}}
		u, _ := url.Parse("https://localhost:" + port)
		if _, _, err := cl.Info(context.Background(), u); err == nil {
			t.Errorf("dns=%s: CLI path answered without the mixnet", mode)
		}
	}
	if n := len(mint.Requests()); n != 0 {
		t.Fatalf("the mint was reached directly %d times", n)
	}
}

// Any web page the user visits can send requests to 127.0.0.1. The status
// page lists the mints the user talks to, so no web page may read it, and
// with --cors set to a list, other origins can't use the gateway at all.
func TestStatusPagePrivateAndOriginAllowlist(t *testing.T) {
	w := newWorld(t)
	_, base := w.gateway(t, nil)
	get(t, base+"/mint.test/v1/info", nil)

	// Default (--cors '*'): wallets from any origin work ...
	resp, _ := get(t, base+"/mint.test/v1/info", map[string]string{"Origin": "https://wallet.example"})
	if resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("default CORS: %d %v", resp.StatusCode, resp.Header)
	}
	// ... but the status and help pages are for local tools only.
	for _, p := range []string{"/.cashumix", "/"} {
		resp, body := get(t, base+p, map[string]string{"Origin": "https://tracker.example"})
		if resp.StatusCode != 403 || strings.Contains(string(body), "mint.test") || resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s from a web page: %d %v %s", p, resp.StatusCode, resp.Header, body)
		}
		resp, body = get(t, base+p, nil)
		if resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s locally: %d %v %s", p, resp.StatusCode, resp.Header, body)
		}
	}

	// --cors https://wallet.example,https://other.example
	n := len(w.mint.Requests())
	_, base = w.gateway(t, func(g *gateway.Gateway, _ *mixnet.TransportOptions) {
		g.CORSOrigin = "https://wallet.example, https://other.example/"
	})
	resp, _ = get(t, base+"/mint.test/v1/info", map[string]string{"Origin": "https://other.example"})
	if resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != "https://other.example" ||
		resp.Header.Get("Vary") != "Origin" {
		t.Fatalf("allowed origin: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = get(t, base+"/mint.test/v1/info", nil) // a CLI wallet sends no Origin
	if resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("no Origin: %d %v", resp.StatusCode, resp.Header)
	}
	if len(w.mint.Requests()) != n+2 {
		t.Fatalf("allowed requests: the mint saw %d, want %d", len(w.mint.Requests()), n+2)
	}
	req, _ := http.NewRequest("OPTIONS", base+"/mint.test/v1/swap", nil)
	req.Header.Set("Origin", "https://wallet.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	if resp, _ := do(t, req); resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != "https://wallet.example" {
		t.Fatalf("allowed preflight: %d %v", resp.StatusCode, resp.Header)
	}
	req.Header.Set("Origin", "https://tracker.example")
	if resp, _ := do(t, req); resp.StatusCode != 403 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("foreign preflight: %d %v", resp.StatusCode, resp.Header)
	}
	req, _ = http.NewRequest("POST", base+"/mint.test/v1/swap", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://tracker.example")
	if resp, _ := do(t, req); resp.StatusCode != 403 {
		t.Fatalf("foreign POST: %d", resp.StatusCode)
	}
	// Browsers don't apply CORS to WebSockets: the gateway checks Origin itself.
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	io.WriteString(conn, "GET /mint.test/v1/ws HTTP/1.1\r\nHost: 127.0.0.1:3338\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nOrigin: https://tracker.example\r\n\r\n")
	wsResp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if wsResp.StatusCode != 403 {
		t.Fatalf("foreign WebSocket: %d", wsResp.StatusCode)
	}
	if len(w.mint.Requests()) != n+2 {
		t.Fatal("a refused request reached the mint")
	}
}
