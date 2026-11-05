package cashumix_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/apeirs/cashumix/cashu"
	"github.com/apeirs/cashumix/gateway"
	"github.com/apeirs/cashumix/mixnet"
	"github.com/apeirs/cashumix/nym"
)

// TestLive talks to public mints through a real nym-socks5-client.
//
//	CASHUMIX_LIVE=1 go test -run Live -v -timeout 20m .
//
// CASHUMIX_NYM_SOCKS5 (default 127.0.0.1:1080) is the client's SOCKS5 port;
// CASHUMIX_LIVE_MINTS overrides the comma-separated mint list.
func TestLive(t *testing.T) {
	if os.Getenv("CASHUMIX_LIVE") != "1" {
		t.Skip("set CASHUMIX_LIVE=1 with nym-socks5-client running (CASHUMIX_NYM_SOCKS5, default 127.0.0.1:1080)")
	}
	proxy, err := mixnet.ParseProxy(envOr("CASHUMIX_NYM_SOCKS5", "127.0.0.1:1080"))
	if err != nil {
		t.Fatal(err)
	}
	mints := strings.Split(envOr("CASHUMIX_LIVE_MINTS", "testnut.cashu.space,mint.minibits.cash/Bitcoin"), ",")
	d := &mixnet.Dialer{Proxy: proxy, DNS: envOr("CASHUMIX_DNS", mixnet.DNSDoH), Trace: t.Logf}
	cl := &cashu.Client{HTTP: &http.Client{Timeout: 4 * time.Minute, Transport: mixnet.NewTransport(d.DialContext, mixnet.TransportOptions{})}}

	t.Run("exit-ip", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		t0 := time.Now()
		ip, err := fetchIP(ctx, cl.HTTP)
		if err != nil {
			t.Fatalf("IP over the mixnet: %v", err)
		}
		fmt.Printf("LIVE exit-ip: servers see %s (%.2fs over the mixnet)\n", ip, time.Since(t0).Seconds())
		// For comparison only: the same question asked directly.
		direct, err := fetchIP(ctx, &http.Client{Timeout: 30 * time.Second, Transport: mixnet.NewTransport(mixnet.Direct(), mixnet.TransportOptions{})})
		if err == nil {
			same := direct == ip
			fmt.Printf("LIVE exit-ip: a direct request comes from this machine's own address (not shown); same as the mixnet one: %v\n", same)
			if same {
				t.Fatal("the mixnet request came from this machine's own IP")
			}
		}
		if n, err := nym.NodeByIP(nym.NymAPI, ip); err == nil && n != nil {
			fmt.Printf("LIVE exit-ip: %s is Nym node %d (%s), exit gateway: %v\n", ip, n.NodeID, n.Description.Aux.Location, n.Description.Roles.ExitNR)
		}
	})

	for _, m := range mints {
		mint, err := cashu.NormalizeMint(m)
		if err != nil {
			t.Fatal(err)
		}
		t.Run("cli/"+mint.Host+mint.Path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			info, call, err := cl.Info(ctx, mint)
			if err != nil {
				t.Fatalf("info: %v", err)
			}
			fmt.Printf("LIVE %s /v1/info over the mixnet: %q %s, nuts %v, %.2fs\n", mint, info.Name, info.Version, info.SupportedNuts(), call.Elapsed.Seconds())
			ks, call, err := cl.Keysets(ctx, mint)
			if err != nil {
				t.Fatalf("keysets: %v", err)
			}
			fmt.Printf("LIVE %s /v1/keysets over the mixnet: %d keysets, %.2fs\n", mint, len(ks), call.Elapsed.Seconds())
			y := randomY(t)
			states, calls, err := cl.CheckState(ctx, mint, []string{y})
			if err != nil {
				t.Fatalf("checkstate: %v", err)
			}
			if states[0].State != "UNSPENT" {
				t.Fatalf("a never-issued proof is %s", states[0].State)
			}
			fmt.Printf("LIVE %s /v1/checkstate over the mixnet: random Y %s... is %s, %.2fs\n", mint, y[:16], states[0].State, calls[0].Elapsed.Seconds())
		})
	}

	t.Run("gateway", func(t *testing.T) {
		g := &gateway.Gateway{Transport: mixnet.NewTransport(d.DialContext, mixnet.TransportOptions{Fresh: true}), Mode: "live", Log: t.Logf}
		srv := httptest.NewServer(g)
		defer srv.Close()
		hc := &http.Client{Timeout: 5 * time.Minute}
		for _, m := range mints {
			base := srv.URL + "/" + strings.TrimSuffix(strings.TrimPrefix(m, "https://"), "/")
			t0 := time.Now()
			resp, err := hc.Get(base + "/v1/info")
			if err != nil {
				t.Fatalf("%s: %v", m, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var info cashu.Info
			if resp.StatusCode != 200 || json.Unmarshal(body, &info) != nil || info.Pubkey == "" {
				t.Fatalf("%s via gateway: HTTP %d %.200s", m, resp.StatusCode, body)
			}
			fmt.Printf("LIVE gateway %s/v1/info -> %q, %.2fs (fresh mixnet stream)\n", base, info.Name, time.Since(t0).Seconds())
			y := randomY(t)
			t0 = time.Now()
			resp, err = hc.Post(base+"/v1/checkstate", "application/json", strings.NewReader(`{"Ys":["`+y+`"]}`))
			if err != nil {
				t.Fatalf("%s checkstate: %v", m, err)
			}
			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || !strings.Contains(string(body), "UNSPENT") {
				t.Fatalf("%s checkstate via gateway: HTTP %d %.200s", m, resp.StatusCode, body)
			}
			fmt.Printf("LIVE gateway %s/v1/checkstate -> UNSPENT, %.2fs\n", base, time.Since(t0).Seconds())
		}
	})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func randomY(t *testing.T) string {
	var b [32]byte
	rand.Read(b[:])
	y, err := cashu.Y(hex.EncodeToString(b[:]))
	if err != nil {
		t.Fatal(err)
	}
	return y
}

func fetchIP(ctx context.Context, hc *http.Client) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.ipify.org", nil)
	req.Header.Set("User-Agent", cashu.UserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	return strings.TrimSpace(string(b)), err
}
