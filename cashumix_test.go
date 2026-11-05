package cashumix_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/apeirs/cashumix/cashu"
	"github.com/apeirs/cashumix/internal/fakes"
	"github.com/apeirs/cashumix/mixnet"
)

// The CLI path: cashu.Client -> mixnet transport -> SOCKS5 -> mint, for a
// real NUT-00 test-vector token whose second proof the mint reports SPENT.
func TestCheckStateOverMixnet(t *testing.T) {
	ca := fakes.NewCA()
	mint := fakes.StartMint(ca, "mint.test")
	defer mint.Close()
	socks := fakes.NewSOCKS(map[string]string{"mint.test:443": mint.Addr})
	defer socks.Close()

	raw, err := os.ReadFile("cashu/testdata/v3.txt")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := cashu.ParseToken(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	ps := tok.Proofs()
	ys := make([]string, len(ps))
	for i, p := range ps {
		ys[i], _ = cashu.Y(p.Secret)
	}
	mint.Spent[ys[1]] = true

	d := &mixnet.Dialer{Proxy: socks.Addr, DNS: mixnet.DNSExit}
	cl := &cashu.Client{HTTP: &http.Client{Timeout: 10 * time.Second,
		Transport: mixnet.NewTransport(d.DialContext, mixnet.TransportOptions{RootCAs: ca.Pool})}}
	m, _ := cashu.NormalizeMint("mint.test")
	states, _, err := cl.CheckState(context.Background(), m, ys)
	if err != nil {
		t.Fatal(err)
	}
	if states[0].State != "UNSPENT" || states[1].State != "SPENT" {
		t.Fatalf("states %+v", states)
	}
	last := mint.Last()
	if last.Path != "/v1/checkstate" || !strings.Contains(string(last.Body), ys[0]) || strings.Contains(string(last.Body), ps[0].Secret) {
		t.Fatalf("mint got %s %s", last.Path, last.Body)
	}
	if seen := socks.Seen(); len(seen) != 1 || seen[0].Host != "mint.test" {
		t.Fatalf("SOCKS saw %v", seen)
	}
}
