// Command cashumix: Cashu wallet <-> mint traffic over the Nym mixnet.
//
//	cashumix nym setup                    start nym-socks5-client on a random active exit gateway
//	cashumix serve                        local mint gateway: http://127.0.0.1:3338/<mint> -> mixnet -> https://<mint>
//	cashumix info | keysets MINT          NUT-06 info / NUT-02 keysets over the mixnet
//	cashumix checkstate [MINT] TOKEN      NUT-07 proof states over the mixnet
//	cashumix decode TOKEN                 offline: show a token's proofs and their Ys
//	cashumix unwrap TOKEN                 offline: put the real mint URL back into a token made via the gateway
//	cashumix ip                           which IP does a server see? (a Nym exit gateway)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/apeirs/cashumix/cashu"
	"github.com/apeirs/cashumix/gateway"
	"github.com/apeirs/cashumix/mixnet"
)

const version = "0.1.0"

var start = time.Now()

func tracef(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[%7.2fs] "+format+"\n", append([]any{time.Since(start).Seconds()}, args...)...)
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, args...)...)
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "cashumix:", err)
	os.Exit(1)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// common flags shared by network commands.
type common struct {
	proxy, dns       string
	direct           bool
	verbose, jsonOut bool
	timeout          time.Duration
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.proxy, "proxy", envOr("CASHUMIX_NYM_SOCKS5", "127.0.0.1:1080"), "nym-socks5-client SOCKS5 address ($CASHUMIX_NYM_SOCKS5)")
	fs.StringVar(&c.dns, "dns", envOr("CASHUMIX_DNS", mixnet.DNSDoH), "name resolution: doh (DoH over the mixnet, connect by IPv4) or exit (the exit gateway resolves)")
	fs.BoolVar(&c.direct, "direct", false, "skip the mixnet and connect directly (comparison and testing only: the mint sees your IP)")
	fs.BoolVar(&c.verbose, "v", false, "trace every mixnet step to stderr")
	fs.BoolVar(&c.jsonOut, "json", false, "JSON output")
	fs.DurationVar(&c.timeout, "timeout", 3*time.Minute, "overall timeout for the command")
}

func (c *common) route() string {
	if c.direct {
		return "DIRECT (no mixnet)"
	}
	return "over the mixnet"
}

func (c *common) dial() (mixnet.DialFunc, string) {
	if c.direct {
		fmt.Fprintln(os.Stderr, "cashumix: WARNING --direct: connecting without the mixnet; the mint sees your IP")
		return mixnet.Direct(), "direct (no mixnet)"
	}
	proxy, err := mixnet.ParseProxy(c.proxy)
	if err != nil {
		die(err)
	}
	if c.dns != mixnet.DNSDoH && c.dns != mixnet.DNSExit {
		die(errors.New("--dns must be doh or exit"))
	}
	d := &mixnet.Dialer{Proxy: proxy, DNS: c.dns}
	if c.verbose {
		d.Trace = tracef
	}
	return d.DialContext, fmt.Sprintf("Nym mixnet via socks5 %s, dns=%s", proxy, c.dns)
}

func (c *common) httpClient() *http.Client {
	dial, _ := c.dial()
	return &http.Client{Timeout: c.timeout, Transport: mixnet.NewTransport(dial, mixnet.TransportOptions{})}
}

func (c *common) ctx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	return ctx, func() { stop(); cancel() }
}

func (c *common) out(v any, human string) {
	if c.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
		return
	}
	fmt.Println(human)
}

func usage() {
	fmt.Fprint(os.Stderr, `cashumix `+version+`: Cashu mint traffic through the Nym mixnet

Mixnet client (official nym-socks5-client):
  cashumix nym setup [--binary PATH] [--port 1080]   init on a random active exit gateway, then start
  cashumix nym start | stop | status | exits

Local mint gateway for any Cashu wallet (nutshell, cdk-cli, cashu.me, Minibits on the same host):
  cashumix serve [--listen 127.0.0.1:3338] [--default MINT] [--only M1,M2] [--allow-port 443] [--fresh]
     then use mint URLs like  http://127.0.0.1:3338/testnut.cashu.space
                              http://127.0.0.1:3338/mint.minibits.cash/Bitcoin

Talk to a mint directly over the mixnet:
  cashumix info MINT                    NUT-06 mint info
  cashumix keysets MINT                 NUT-02 keysets (id, unit, active, fee)
  cashumix checkstate [MINT] INPUT      NUT-07: are these proofs UNSPENT, PENDING or SPENT?
                                        INPUT: cashuA/cashuB token, a file with one, proofs JSON, or -
  cashumix decode INPUT                 offline: a token's mint, amounts, keyset ids and Ys
  cashumix unwrap INPUT                 offline: replace a gateway mint URL (http://127.0.0.1:3338/<mint>)
                                        in a token with the mint's real https URL, before you send it
  cashumix ip                           which IP do servers see? (should be a Nym exit gateway)

Common flags: --proxy 127.0.0.1:1080 ($CASHUMIX_NYM_SOCKS5) --dns doh|exit -v --json --timeout 3m
              --direct (comparison only: skips the mixnet, the mint sees your IP)
cashumix never holds keys or spends proofs, and there is no clearnet fallback:
if the mixnet client is down, cashumix refuses to connect.
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "nym":
		cmdNym(args)
	case "serve", "gateway":
		cmdServe(args)
	case "info":
		cmdInfo(args)
	case "keysets":
		cmdKeysets(args)
	case "checkstate", "check":
		cmdCheckState(args)
	case "decode":
		cmdDecode(args)
	case "unwrap":
		cmdUnwrap(args)
	case "ip":
		cmdIP(args)
	case "version", "--version":
		fmt.Println("cashumix", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

// parse accepts flags before, between and after positional arguments.
func parse(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			os.Exit(2)
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func oneMint(pos []string, cmd string) *url.URL {
	if len(pos) != 1 {
		die(fmt.Errorf("usage: cashumix %s MINT (e.g. testnut.cashu.space or mint.minibits.cash/Bitcoin)", cmd))
	}
	u, err := cashu.NormalizeMint(pos[0])
	if err != nil {
		die(err)
	}
	return u
}

// ---------------------------------------------------------------- info

func cmdInfo(args []string) {
	var c common
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	c.register(fs)
	mint := oneMint(parse(fs, args), "info")
	ctx, cancel := c.ctx()
	defer cancel()
	cl := &cashu.Client{HTTP: c.httpClient()}
	info, call, err := cl.Info(ctx, mint)
	if err != nil {
		die(fmt.Errorf("%s %s: %w", call.URL, c.route(), err))
	}
	var nuts []string
	for _, n := range info.SupportedNuts() {
		nuts = append(nuts, strconv.Itoa(n))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)\n", info.Name, info.Version)
	fmt.Fprintf(&b, "  url          %s\n", mint)
	fmt.Fprintf(&b, "  pubkey       %s\n", info.Pubkey)
	if info.Description != "" {
		fmt.Fprintf(&b, "  description  %s\n", oneLine(info.Description, 100))
	}
	if info.Motd != "" {
		fmt.Fprintf(&b, "  motd         %s\n", oneLine(info.Motd, 100))
	}
	fmt.Fprintf(&b, "  nuts         %s\n", strings.Join(nuts, " "))
	fmt.Fprintf(&b, "fetched %s in %.2fs", c.route(), call.Elapsed.Seconds())
	c.out(map[string]any{"mint": mint.String(), "info": info, "supported_nuts": info.SupportedNuts(),
		"elapsed_s": call.Elapsed.Seconds(), "route": c.route()}, b.String())
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n-3] + "..."
	}
	return s
}

// ---------------------------------------------------------------- keysets

func cmdKeysets(args []string) {
	var c common
	fs := flag.NewFlagSet("keysets", flag.ExitOnError)
	c.register(fs)
	mint := oneMint(parse(fs, args), "keysets")
	ctx, cancel := c.ctx()
	defer cancel()
	cl := &cashu.Client{HTTP: c.httpClient()}
	ks, call, err := cl.Keysets(ctx, mint)
	if err != nil {
		die(fmt.Errorf("%s %s: %w", call.URL, c.route(), err))
	}
	if c.jsonOut {
		c.out(map[string]any{"mint": mint.String(), "keysets": ks, "elapsed_s": call.Elapsed.Seconds(), "route": c.route()}, "")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "KEYSET ID\tUNIT\tACTIVE\tINPUT FEE (ppk)")
	for _, k := range ks {
		fmt.Fprintf(tw, "%s\t%s\t%v\t%d\n", k.ID, k.Unit, k.Active, k.InputFeePPK)
	}
	tw.Flush()
	fmt.Printf("%d keysets from %s, fetched %s in %.2fs\n", len(ks), mint, c.route(), call.Elapsed.Seconds())
}

// ---------------------------------------------------------------- checkstate

// readInput loads a token or proofs from an argument: "-" (stdin), a file
// path, or the token / JSON itself.
func readInput(arg string) (*cashu.Token, error) {
	if arg == "-" {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<20))
		if err != nil {
			return nil, err
		}
		return cashu.ParseInput(b)
	}
	if st, err := os.Stat(arg); err == nil && st.Mode().IsRegular() {
		b, err := os.ReadFile(arg)
		if err != nil {
			return nil, err
		}
		return cashu.ParseInput(b)
	}
	tok, err := cashu.ParseInput([]byte(arg))
	if errors.Is(err, cashu.ErrNotToken) {
		return nil, fmt.Errorf("%.40s... is neither a token nor a readable file", arg)
	}
	return tok, err
}

type checkGroup struct {
	mint   *url.URL
	proofs []cashu.Proof
}

// groups decides which mint each proof is checked at: the MINT argument for
// all of them, or else the mint named in the token.
func groups(tok *cashu.Token, explicit string) ([]checkGroup, error) {
	if explicit != "" {
		u, err := cashu.NormalizeMint(explicit)
		if err != nil {
			return nil, err
		}
		return []checkGroup{{u, tok.Proofs()}}, nil
	}
	var out []checkGroup
	idx := map[string]int{}
	for _, e := range tok.Entries {
		if e.Mint == "" {
			return nil, errors.New("the input names no mint; give it first: cashumix checkstate MINT INPUT")
		}
		u, err := cashu.NormalizeMint(e.Mint)
		if err != nil {
			return nil, fmt.Errorf("mint in the token: %w", err)
		}
		i, ok := idx[u.String()]
		if !ok {
			i = len(out)
			idx[u.String()] = i
			out = append(out, checkGroup{mint: u})
		}
		out[i].proofs = append(out[i].proofs, e.Proofs...)
	}
	return out, nil
}

type checked struct {
	Mint   string `json:"mint"`
	Y      string `json:"Y"`
	Amount uint64 `json:"amount"`
	ID     string `json:"id"`
	State  string `json:"state"`
}

func cmdCheckState(args []string) {
	var c common
	fs := flag.NewFlagSet("checkstate", flag.ExitOnError)
	c.register(fs)
	pos := parse(fs, args)
	var mintArg, input string
	switch len(pos) {
	case 1:
		input = pos[0]
	case 2:
		mintArg, input = pos[0], pos[1]
	default:
		die(errors.New("usage: cashumix checkstate [MINT] TOKEN|FILE|-"))
	}
	tok, err := readInput(input)
	if err != nil {
		die(err)
	}
	gs, err := groups(tok, mintArg)
	if err != nil {
		die(err)
	}
	ctx, cancel := c.ctx()
	defer cancel()
	cl := &cashu.Client{HTTP: c.httpClient()}
	var rows []checked
	totals := map[string]uint64{}
	var elapsed time.Duration
	for _, g := range gs {
		ys := make([]string, len(g.proofs))
		for i, p := range g.proofs {
			if ys[i], err = cashu.Y(p.Secret); err != nil {
				die(err)
			}
		}
		states, calls, err := cl.CheckState(ctx, g.mint, ys)
		for _, call := range calls {
			elapsed += call.Elapsed
		}
		if err != nil {
			die(fmt.Errorf("checkstate at %s %s: %w", g.mint, c.route(), err))
		}
		for i, s := range states {
			rows = append(rows, checked{Mint: g.mint.String(), Y: ys[i], Amount: g.proofs[i].Amount, ID: g.proofs[i].ID, State: s.State})
			totals[s.State] += g.proofs[i].Amount
		}
	}
	unit := tok.Unit
	if unit == "" {
		unit = "sat"
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "Y\tAMOUNT\tKEYSET\tSTATE")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", r.Y, r.Amount, r.ID, r.State)
	}
	tw.Flush()
	var parts []string
	for _, st := range []string{"UNSPENT", "PENDING", "SPENT"} {
		if totals[st] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s %s", totals[st], unit, st))
		}
	}
	mints := make([]string, len(gs))
	for i, g := range gs {
		mints[i] = g.mint.String()
	}
	fmt.Fprintf(&b, "%d proofs at %s: %s (checked %s in %.2fs)", len(rows), strings.Join(mints, ", "), strings.Join(parts, ", "), c.route(), elapsed.Seconds())
	c.out(map[string]any{"proofs": rows, "totals": totals, "unit": unit, "elapsed_s": elapsed.Seconds(), "route": c.route()}, b.String())
}

// ---------------------------------------------------------------- decode

func cmdDecode(args []string) {
	fs := flag.NewFlagSet("decode", flag.ExitOnError)
	secrets := fs.Bool("secrets", false, "include proof secrets and signatures (anyone with them can spend the token)")
	pos := parse(fs, args)
	if len(pos) != 1 {
		die(errors.New("usage: cashumix decode TOKEN|FILE|-"))
	}
	tok, err := readInput(pos[0])
	if err != nil {
		die(err)
	}
	type proofOut struct {
		Amount uint64 `json:"amount"`
		ID     string `json:"id"`
		Y      string `json:"Y"`
		Secret string `json:"secret,omitempty"`
		C      string `json:"C,omitempty"`
	}
	type entryOut struct {
		Mint   string     `json:"mint"`
		Proofs []proofOut `json:"proofs"`
	}
	out := struct {
		Format  string     `json:"format"`
		Unit    string     `json:"unit,omitempty"`
		Memo    string     `json:"memo,omitempty"`
		Amount  uint64     `json:"amount"`
		Entries []entryOut `json:"token"`
	}{Format: tok.Format, Unit: tok.Unit, Memo: tok.Memo, Amount: tok.Amount()}
	for _, e := range tok.Entries {
		eo := entryOut{Mint: e.Mint}
		for _, p := range e.Proofs {
			y, err := cashu.Y(p.Secret)
			if err != nil {
				die(err)
			}
			po := proofOut{Amount: p.Amount, ID: p.ID, Y: y}
			if *secrets {
				po.Secret, po.C = p.Secret, p.C
			}
			eo.Proofs = append(eo.Proofs, po)
		}
		out.Entries = append(out.Entries, eo)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

// ---------------------------------------------------------------- unwrap

// cmdUnwrap fixes tokens made by a wallet that uses the gateway: their mint
// URL is http://127.0.0.1:3338/<mint>, which only works on this machine.
func cmdUnwrap(args []string) {
	fs := flag.NewFlagSet("unwrap", flag.ExitOnError)
	pos := parse(fs, args)
	if len(pos) != 1 {
		die(errors.New("usage: cashumix unwrap TOKEN|FILE|-"))
	}
	tok, err := readInput(pos[0])
	if err != nil {
		die(err)
	}
	changed := 0
	for i, e := range tok.Entries {
		if real, ok := cashu.UnwrapGatewayURL(e.Mint); ok {
			fmt.Fprintf(os.Stderr, "mint %s -> %s\n", e.Mint, real)
			tok.Entries[i].Mint = real
			changed++
		}
	}
	if changed == 0 {
		fmt.Fprintln(os.Stderr, "cashumix: no gateway mint URL in this token; re-encoding it unchanged")
	}
	out, err := tok.EncodeV4()
	if err != nil {
		die(err)
	}
	fmt.Println(out)
}

// ---------------------------------------------------------------- serve

func cmdServe(args []string) {
	var c common
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	c.register(fs)
	listen := fs.String("listen", envOr("CASHUMIX_LISTEN", gateway.DefaultListen), "local address ($CASHUMIX_LISTEN)")
	def := fs.String("default", "", "mint used for paths without a mint host, e.g. mint.minibits.cash/Bitcoin (optional)")
	only := fs.String("only", "", "comma-separated mint hosts; refuse all others")
	ports := fs.String("allow-port", "443", "comma-separated upstream ports allowed (the Nym exit policy must allow them too)")
	fresh := fs.Bool("fresh", false, "new mixnet stream and TLS session for every request (no connection reuse the mint could link; slower)")
	cors := fs.String("cors", "*", `Access-Control-Allow-Origin for browser wallets ("-" = no CORS headers)`)
	hosts := fs.String("host", "", "extra Host header names to accept besides loopback (DNS-rebinding guard)")
	public := fs.Bool("public", false, "allow listening on a non-loopback address (anyone who can reach it uses your mixnet client)")
	ua := fs.String("user-agent", cashu.UserAgent, "User-Agent sent to every mint, whatever the wallet sends")
	maxBody := fs.Int64("max-body", gateway.DefaultMaxRequestBody, "largest request body in bytes")
	maxResp := fs.Int64("max-response", gateway.DefaultMaxResponseBody, "largest mint response in bytes")
	reqTO := fs.Duration("request-timeout", gateway.DefaultTimeout, "per-request timeout (the mixnet is slow; WebSockets are exempt)")
	quiet := fs.Bool("q", false, "no access log")
	if pos := parse(fs, args); len(pos) > 0 {
		die(fmt.Errorf("serve takes no arguments (got %q)", pos))
	}

	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		die(fmt.Errorf("--listen: %w", err))
	}
	if ip := net.ParseIP(host); (ip == nil && host != "localhost") || (ip != nil && !ip.IsLoopback()) {
		if !*public {
			die(fmt.Errorf("refusing to listen on %s: not a loopback address (pass --public if you really mean it)", host))
		}
	}
	dial, mode := c.dial()
	g := &gateway.Gateway{
		Transport:       mixnet.NewTransport(dial, mixnet.TransportOptions{Fresh: *fresh, HeaderTimeout: *reqTO}),
		UserAgent:       *ua,
		CORSOrigin:      *cors,
		Hosts:           splitList(*hosts),
		MaxRequestBody:  *maxBody,
		MaxResponseBody: *maxResp,
		Timeout:         *reqTO,
		Mode:            mode,
		Log:             logf,
		Ports:           map[int]bool{},
	}
	if *quiet {
		g.Log = nil
	}
	if *fresh {
		g.Mode += ", fresh stream per request"
	}
	if *def != "" {
		u, err := cashu.NormalizeMint(*def)
		if err != nil {
			die(fmt.Errorf("--default: %w", err))
		}
		g.Default = u.String()
	}
	for _, p := range splitList(*ports) {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			die(fmt.Errorf("--allow-port: bad port %q", p))
		}
		g.Ports[n] = true
	}
	if *only != "" {
		g.Only = map[string]bool{}
		for _, h := range splitList(*only) {
			u, err := cashu.NormalizeMint(h)
			if err != nil {
				die(fmt.Errorf("--only: %w", err))
			}
			g.Only[strings.ToLower(u.Hostname())] = true
		}
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		die(err)
	}
	if !c.direct {
		proxy, _ := mixnet.ParseProxy(c.proxy)
		if conn, err := net.DialTimeout("tcp", proxy, 2*time.Second); err != nil {
			logf("WARNING: no SOCKS5 proxy at %s yet (start it with `cashumix nym setup` or `cashumix nym start`); requests will fail until it is up. There is no clearnet fallback.", proxy)
		} else {
			conn.Close()
		}
	}
	addr := ln.Addr().String()
	logf("cashumix %s gateway on http://%s  ->  %s  ->  https mints", version, addr, mode)
	logf("set your wallet's mint URL to http://%s/<mint>, e.g. http://%s/testnut.cashu.space", addr, addr)
	if g.Default != "" {
		logf("paths without a mint host go to %s", g.Default)
	}
	srv := &http.Server{Handler: g, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		die(err)
	}
	logf("gateway stopped")
}
