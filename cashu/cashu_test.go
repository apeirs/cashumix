package cashu

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readVector(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// NUT-00 test vectors, "Hash-to-curve function" (tests/00-tests.md).
func TestHashToCurveVectors(t *testing.T) {
	for _, v := range []struct{ msg, point string }{
		{"0000000000000000000000000000000000000000000000000000000000000000", "024cce997d3b518f739663b757deaec95bcd9473c30a14ac2fd04023a739d1a725"},
		{"0000000000000000000000000000000000000000000000000000000000000001", "022e7158e11c9506f1aa4248bf531298daa7febd6194f003edcd9b93ade6253acf"},
		// Takes a few iterations of the counter loop.
		{"0000000000000000000000000000000000000000000000000000000000000002", "026cdbe15362df59cd1dd3c9c11de8aedac2106eca69236ecd9fbe117af897be4f"},
	} {
		pk, err := HashToCurve(unhex(t, v.msg))
		if err != nil {
			t.Fatal(err)
		}
		if got := hex.EncodeToString(pk.SerializeCompressed()); got != v.point {
			t.Errorf("hash_to_curve(%s) = %s, want %s", v.msg, got, v.point)
		}
	}
}

// NUT-00 test vectors, "Blinded messages": B_ = hash_to_curve(x) + r*G.
// They exercise hash_to_curve on random-looking messages too.
func TestBlindedMessageVectors(t *testing.T) {
	for _, v := range []struct{ x, r, b string }{
		{"d341ee4871f1f889041e63cf0d3823c713eea6aff01e80f1719f08f9e5be98f6", "99fce58439fc37412ab3468b73db0569322588f62fb3a49182d67e23d877824a", "033b1a9737a40cc3fd9b6af4b723632b76a67a36782596304612a6c2bfb5197e6d"},
		{"f1aaf16c2239746f369572c0784d9dd3d032d952c2d992175873fb58fae31a60", "f78476ea7cc9ade20f9e05e58a804cf19533f03ea805ece5fee88c8e2874ba50", "029bdf2d716ee366eddf599ba252786c1033f47e230248a4612a5670ab931f1763"},
	} {
		y, err := HashToCurve(unhex(t, v.x))
		if err != nil {
			t.Fatal(err)
		}
		var yj, rg, sum secp256k1.JacobianPoint
		y.AsJacobian(&yj)
		r := secp256k1.PrivKeyFromBytes(unhex(t, v.r))
		r.PubKey().AsJacobian(&rg)
		secp256k1.AddNonConst(&yj, &rg, &sum)
		sum.ToAffine()
		got := hex.EncodeToString(secp256k1.NewPublicKey(&sum.X, &sum.Y).SerializeCompressed())
		if got != v.b {
			t.Errorf("B_ for x=%s: got %s, want %s", v.x, got, v.b)
		}
	}
}

func TestYUsesSecretBytes(t *testing.T) {
	// Y is hash_to_curve of the secret's UTF-8 bytes, not of its hex decoding.
	secret := "407915bc212be61a77e3e6d2aeb4c727980bda51cd06a6afc29e2861768a7837"
	y, err := Y(secret)
	if err != nil {
		t.Fatal(err)
	}
	pk, _ := HashToCurve([]byte(secret))
	if y != hex.EncodeToString(pk.SerializeCompressed()) || len(y) != 66 || (y[:2] != "02" && y[:2] != "03") {
		t.Fatalf("Y = %s", y)
	}
	other, _ := HashToCurve(unhex(t, secret))
	if hex.EncodeToString(other.SerializeCompressed()) == y {
		t.Fatal("Y must not hex-decode the secret")
	}
	if y2, _ := Y(secret + " "); y2 == y {
		t.Fatal("different secrets gave the same Y")
	}
}

func TestTokenV3Vectors(t *testing.T) {
	tok, err := ParseToken(readVector(t, "v3.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if tok.Format != "cashuA" || tok.Unit != "sat" || tok.Memo != "Thank you." || tok.Amount() != 10 {
		t.Fatalf("decoded %+v", tok)
	}
	if m := tok.Mints(); len(m) != 1 || m[0] != "https://8333.space:3338" {
		t.Fatalf("mints %v", m)
	}
	ps := tok.Proofs()
	if len(ps) != 2 || ps[0].Amount != 2 || ps[0].ID != "009a1f293253e41e" ||
		ps[0].Secret != "407915bc212be61a77e3e6d2aeb4c727980bda51cd06a6afc29e2861768a7837" ||
		ps[1].C != "029e8e5050b890a7d6c0968db16bc1d5d5fa040ea1de284f6ec69d61299f671059" {
		t.Fatalf("proofs %+v", ps)
	}
	for _, f := range []string{"v3-padded.txt", "v3-unpadded.txt"} {
		tok, err := ParseToken(readVector(t, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if tok.Memo != "Thank you very much." || len(tok.Proofs()) != 2 {
			t.Fatalf("%s: %+v", f, tok)
		}
	}
	for _, f := range []string{"v3-bad-prefix.txt", "v3-no-prefix.txt"} {
		if _, err := ParseToken(readVector(t, f)); !errors.Is(err, ErrNotToken) {
			t.Fatalf("%s: want ErrNotToken, got %v", f, err)
		}
	}
	// Wallets also share tokens as cashu: URIs, and pasted tokens get wrapped.
	wrapped := "cashu:" + readVector(t, "v3.txt")[:100] + "\n" + readVector(t, "v3.txt")[100:]
	if tok, err := ParseToken(wrapped); err != nil || tok.Amount() != 10 {
		t.Fatalf("cashu: URI / wrapped: %v", err)
	}
}

func TestTokenV4Vectors(t *testing.T) {
	tok, err := ParseToken(readVector(t, "v4-single.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ps := tok.Proofs()
	if tok.Format != "cashuB" || tok.Memo != "Thank you" || tok.Unit != "sat" || len(ps) != 1 ||
		tok.Mints()[0] != "http://localhost:3338" || ps[0].Amount != 1 || ps[0].ID != "00ad268c4d1f5826" ||
		ps[0].Secret != "9a6dbb847bd232ba76db0df197216b29d3b8cc14553cd27827fc1cc942fedb4e" ||
		ps[0].C != "038618543ffb6b8695df4ad4babcde92a34a96bdcd97dcee0d7ccf98d472126792" {
		t.Fatalf("single keyset: %+v", tok)
	}

	tok, err = ParseToken(readVector(t, "v4-multi.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ps = tok.Proofs()
	want := []struct {
		a     uint64
		id, s string
		c     string
	}{
		{1, "00ffd48b8f5ecf80", "acc12435e7b8484c3cf1850149218af90f716a52bf4a5ed347e48ecc13f77388", "0244538319de485d55bed3b29a642bee5879375ab9e7a620e11e48ba482421f3cf"},
		{2, "00ad268c4d1f5826", "1323d3d4707a58ad2e23ada4e9f1f49f5a5b4ac7b708eb0d61f738f48307e8ee", "023456aa110d84b4ac747aebd82c3b005aca50bf457ebd5737a4414fac3ae7d94d"},
		{1, "00ad268c4d1f5826", "56bcbcbb7cc6406b3fa5d57d2174f4eff8b4402b176926d3a57d3c3dcbb59d57", "0273129c5719e599379a974a626363c333c56cafc0e6d01abe46d5808280789c63"},
	}
	if len(ps) != len(want) || tok.Memo != "" || tok.Amount() != 4 {
		t.Fatalf("multi keyset: %+v", tok)
	}
	for i, w := range want {
		if ps[i].Amount != w.a || ps[i].ID != w.id || ps[i].Secret != w.s || ps[i].C != w.c {
			t.Errorf("proof %d = %+v, want %+v", i, ps[i], w)
		}
	}

	// Raw binary serialization of the single-keyset token.
	raw, err := ParseInput(unhex(t, readVector(t, "v4-raw.hex")))
	if err != nil {
		t.Fatal(err)
	}
	if raw.Proofs()[0].Secret != "9a6dbb847bd232ba76db0df197216b29d3b8cc14553cd27827fc1cc942fedb4e" || raw.Memo != "Thank you" {
		t.Fatalf("crawB: %+v", raw)
	}
}

func TestParseInputForms(t *testing.T) {
	v3 := readVector(t, "v3.txt")
	proofs := `[{"amount":2,"id":"009a1f293253e41e","secret":"s1","C":"02bc"},{"amount":8,"id":"009a1f293253e41e","secret":"s2","C":"029e"}]`
	for name, in := range map[string]string{
		"token":        "  " + v3 + "\n",
		"proof array":  proofs,
		"proofs obj":   `{"mint":"https://m.example","proofs":` + proofs + `}`,
		"v3 json":      `{"token":[{"mint":"https://m.example","proofs":` + proofs + `}],"unit":"sat"}`,
		"witness obj":  `[{"amount":2,"id":"00","secret":"s","C":"02","witness":{"signatures":["ab"]}}]`,
		"witness text": `[{"amount":2,"id":"00","secret":"s","C":"02","witness":"{\"signatures\":[]}"}]`,
	} {
		tok, err := ParseInput([]byte(in))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(tok.Proofs()) == 0 {
			t.Errorf("%s: no proofs", name)
		}
	}
	for name, in := range map[string]string{
		"empty":      "  ",
		"no proofs":  `{"proofs":[]}`,
		"no secret":  `[{"amount":1,"id":"00","C":"02"}]`,
		"garbage":    "hello",
		"bad base64": "cashuA%%%",
		"bad cbor":   "cashuBpGF0",
	} {
		if _, err := ParseInput([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCBOR(t *testing.T) {
	for _, c := range []struct {
		hex  string
		want string // JSON of the decoded value ([]byte shows as base64)
	}{
		{"00", "0"}, {"17", "23"}, {"1818", "24"}, {"1903e8", "1000"}, {"1b000000e8d4a51000", "1000000000000"},
		{"20", "-1"}, {"3903e7", "-1000"}, {"f4", "false"}, {"f5", "true"}, {"f6", "null"},
		{"f93c00", "1"}, {"fa47c35000", "100000"}, {"fb3ff199999999999a", "1.1"},
		{"6449455446", `"IETF"`}, {"4401020304", `"AQIDBA=="`}, {"5f42010243030405ff", `"AQIDBAU="`},
		{"7f657374726561646d696e67ff", `"streaming"`}, {"83010203", "[1,2,3]"}, {"9f018202039f0405ffff", "[1,[2,3],[4,5]]"},
		{"a26161016162820203", `{"a":1,"b":[2,3]}`}, {"bf61610161629f0203ffff", `{"a":1,"b":[2,3]}`},
		{"c11a514b67b0", "1363896240"}, {"80", "[]"}, {"a0", "{}"},
	} {
		v, err := decodeCBOR(unhex(t, c.hex))
		if err != nil {
			t.Errorf("%s: %v", c.hex, err)
			continue
		}
		got, _ := json.Marshal(v)
		if string(got) != c.want {
			t.Errorf("%s: got %s, want %s", c.hex, got, c.want)
		}
	}
	for _, bad := range []string{"", "18", "1a0000", "6261", "83 0102", "a1016161", "a2616101616102", "ff", "1c", "9b00000000ffffffff", "bb00000000ffffffff", "0000", "5f01ff"} {
		if _, err := decodeCBOR(unhex(t, strings.ReplaceAll(bad, " ", ""))); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
	deep := strings.Repeat("81", 40) + "00"
	if _, err := decodeCBOR(unhex(t, deep)); err == nil {
		t.Error("deep nesting accepted")
	}
}

func TestNormalizeMint(t *testing.T) {
	for in, want := range map[string]string{
		"testnut.cashu.space":                 "https://testnut.cashu.space",
		"mint.minibits.cash/Bitcoin":          "https://mint.minibits.cash/Bitcoin",
		"https://mint.minibits.cash/Bitcoin/": "https://mint.minibits.cash/Bitcoin",
		"https://8333.space:3338":             "https://8333.space:3338",
		" https://mint.example.com/ ":         "https://mint.example.com",
	} {
		u, err := NormalizeMint(in)
		if err != nil || u.String() != want {
			t.Errorf("NormalizeMint(%q) = %v, %v; want %s", in, u, err, want)
		}
	}
	for _, bad := range []string{"", "http://mint.example.com", "https://", "https://u:p@m.example", "https://m.example/?x=1", "ftp://m.example"} {
		if _, err := NormalizeMint(bad); err == nil {
			t.Errorf("NormalizeMint(%q) accepted", bad)
		}
	}
}

func TestClientAgainstMint(t *testing.T) {
	spent := map[string]bool{}
	y1, _ := Y("first")
	y2, _ := Y("second")
	spent[y2] = true
	var gotUA []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = append(gotUA, r.Header.Get("User-Agent"))
		switch r.URL.Path {
		case "/Bitcoin/v1/info":
			w.Write([]byte(`{"name":"Test","version":"x/1","pubkey":"02aa","nuts":{"4":{"methods":[],"disabled":false},"5":{"methods":[],"disabled":true},"7":{"supported":true},"8":{"supported":false},"15":[]}}`))
		case "/Bitcoin/v1/keysets":
			w.Write([]byte(`{"keysets":[{"id":"00ad268c4d1f5826","unit":"sat","active":true,"input_fee_ppk":100}]}`))
		case "/Bitcoin/v1/checkstate":
			var req struct{ Ys []string }
			if json.NewDecoder(r.Body).Decode(&req) != nil || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
				w.WriteHeader(400)
				w.Write([]byte(`{"detail":"bad request","code":10000}`))
				return
			}
			var states []map[string]any
			for i := len(req.Ys) - 1; i >= 0; i-- { // reversed: the client must match by Y
				st := "UNSPENT"
				if spent[req.Ys[i]] {
					st = "SPENT"
				}
				states = append(states, map[string]any{"Y": req.Ys[i], "state": st, "witness": nil})
			}
			json.NewEncoder(w).Encode(map[string]any{"states": states})
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"detail":"not found","code":10001}`))
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client()}
	mint, _ := NormalizeMint(srv.URL + "/Bitcoin")
	ctx := context.Background()

	info, call, err := c.Info(ctx, mint)
	if err != nil || info.Name != "Test" || call.Status != 200 {
		t.Fatalf("info: %+v %v", info, err)
	}
	if got, _ := json.Marshal(info.SupportedNuts()); string(got) != "[4,7,15]" {
		t.Fatalf("supported nuts %s", got)
	}
	ks, _, err := c.Keysets(ctx, mint)
	if err != nil || len(ks) != 1 || ks[0].InputFeePPK != 100 || !ks[0].Active {
		t.Fatalf("keysets %+v %v", ks, err)
	}
	states, calls, err := c.CheckState(ctx, mint, []string{y1, y2})
	if err != nil || len(calls) != 1 || states[0].Y != y1 || states[0].State != "UNSPENT" || states[1].State != "SPENT" {
		t.Fatalf("checkstate %+v %v", states, err)
	}
	// Batching: 250 Ys -> 3 requests.
	many := make([]string, 250)
	for i := range many {
		many[i], _ = Y(strings.Repeat("x", i+1))
	}
	if states, calls, err := c.CheckState(ctx, mint, many); err != nil || len(calls) != 3 || len(states) != 250 {
		t.Fatalf("batched: %d calls, %d states, %v", len(calls), len(states), err)
	}
	other, _ := NormalizeMint(srv.URL + "/nope")
	_, _, err = c.Info(ctx, other)
	var me *MintError
	if !errors.As(err, &me) || me.Code != 10001 || me.Status != 404 || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("mint error: %v", err)
	}
	for _, ua := range gotUA {
		if ua != UserAgent {
			t.Fatalf("User-Agent %q", ua)
		}
	}
}

func TestEncodeV4RoundTrip(t *testing.T) {
	for _, f := range []string{"v4-single.txt", "v4-multi.txt"} {
		in := readVector(t, f)
		tok, err := ParseToken(in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := tok.EncodeV4()
		if err != nil {
			t.Fatal(err)
		}
		// The spec's single-keyset vector carries base64 padding; we emit none.
		if out != strings.TrimRight(in, "=") {
			t.Errorf("%s: re-encoded\n%s\nwant\n%s", f, out, in)
		}
	}
	// A V3 token converts to V4 and back to the same proofs.
	v3, _ := ParseToken(readVector(t, "v3.txt"))
	s, err := v3.EncodeV4()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseToken(s)
	if err != nil || back.Amount() != 10 || back.Memo != "Thank you." || back.Proofs()[1].C != v3.Proofs()[1].C {
		t.Fatalf("v3 -> v4: %+v %v", back, err)
	}
	two := &Token{Entries: []TokenEntry{{Mint: "https://a.example", Proofs: v3.Proofs()}, {Mint: "https://b.example", Proofs: v3.Proofs()}}}
	if _, err := two.EncodeV4(); err == nil {
		t.Fatal("two mints in one cashuB token")
	}
}

func TestUnwrapGatewayURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:3338/testnut.cashu.space":         "https://testnut.cashu.space",
		"http://localhost:3338/mint.minibits.cash/Bitcoin/": "https://mint.minibits.cash/Bitcoin",
		"http://[::1]:3338/https://mint.example.com":        "https://mint.example.com",
	} {
		got, ok := UnwrapGatewayURL(in)
		if !ok || got != want {
			t.Errorf("UnwrapGatewayURL(%q) = %q %v", in, got, ok)
		}
		if u, err := NormalizeMint(in); err != nil || u.String() != want {
			t.Errorf("NormalizeMint(%q) = %v %v", in, u, err)
		}
	}
	for _, in := range []string{"http://localhost:3338", "http://localhost:3338/v1", "https://127.0.0.1/mint.example.com", "http://10.0.0.1:3338/mint.example.com"} {
		if got, ok := UnwrapGatewayURL(in); ok {
			t.Errorf("UnwrapGatewayURL(%q) = %q", in, got)
		}
	}
}
