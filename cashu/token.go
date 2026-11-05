package cashu

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// DLEQ is a NUT-12 proof attached to a Proof (hex strings).
type DLEQ struct {
	E string `json:"e"`
	S string `json:"s"`
	R string `json:"r,omitempty"`
}

// Proof is a NUT-00 proof as wallets store and send it.
type Proof struct {
	Amount  uint64          `json:"amount"`
	ID      string          `json:"id"`
	Secret  string          `json:"secret"`
	C       string          `json:"C"`
	Witness json.RawMessage `json:"witness,omitempty"`
	DLEQ    *DLEQ           `json:"dleq,omitempty"`
}

// TokenEntry is a set of proofs from one mint.
type TokenEntry struct {
	Mint   string  `json:"mint"`
	Proofs []Proof `json:"proofs"`
}

// Token is a decoded cashuA (V3) or cashuB (V4) token, or a plain proof list.
type Token struct {
	Format  string       `json:"format"` // "cashuA", "cashuB" or "proofs"
	Entries []TokenEntry `json:"token"`
	Unit    string       `json:"unit,omitempty"`
	Memo    string       `json:"memo,omitempty"`
}

// Proofs returns every proof in the token.
func (t *Token) Proofs() []Proof {
	var out []Proof
	for _, e := range t.Entries {
		out = append(out, e.Proofs...)
	}
	return out
}

// Amount is the sum of all proof amounts.
func (t *Token) Amount() uint64 {
	var n uint64
	for _, p := range t.Proofs() {
		n += p.Amount
	}
	return n
}

// Mints lists the distinct mint URLs named in the token, in order.
func (t *Token) Mints() []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range t.Entries {
		if e.Mint != "" && !seen[e.Mint] {
			seen[e.Mint] = true
			out = append(out, e.Mint)
		}
	}
	return out
}

// ErrNotToken means the input is neither a cashuA/cashuB token nor proof JSON.
var ErrNotToken = errors.New("not a Cashu token (expected cashuA..., cashuB..., or proof JSON)")

// decodeB64 accepts URL-safe or standard base64, with or without padding,
// and ignores whitespace (tokens get wrapped when pasted).
func decodeB64(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		case '+':
			return '-'
		case '/':
			return '_'
		}
		return r
	}, s)
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// ParseToken decodes a serialized token: "cashuA" + base64url(JSON) (V3) or
// "cashuB" + base64url(CBOR) (V4). A "cashu:" URI prefix is accepted.
func ParseToken(s string) (*Token, error) {
	s = strings.TrimSpace(s)
	if len(s) > 6 && strings.EqualFold(s[:6], "cashu:") {
		s = s[6:]
	}
	switch {
	case strings.HasPrefix(s, "cashuA"):
		raw, err := decodeB64(s[6:])
		if err != nil {
			return nil, fmt.Errorf("cashuA token: bad base64: %w", err)
		}
		t, err := parseV3JSON(raw)
		if err != nil {
			return nil, fmt.Errorf("cashuA token: %w", err)
		}
		t.Format = "cashuA"
		return t, nil
	case strings.HasPrefix(s, "cashuB"):
		raw, err := decodeB64(s[6:])
		if err != nil {
			return nil, fmt.Errorf("cashuB token: bad base64: %w", err)
		}
		t, err := ParseTokenV4(raw)
		if err != nil {
			return nil, fmt.Errorf("cashuB token: %w", err)
		}
		return t, nil
	}
	return nil, ErrNotToken
}

func parseV3JSON(raw []byte) (*Token, error) {
	var v3 struct {
		Token []TokenEntry `json:"token"`
		Unit  string       `json:"unit"`
		Memo  string       `json:"memo"`
	}
	if err := json.Unmarshal(raw, &v3); err != nil {
		return nil, fmt.Errorf("bad JSON: %w", err)
	}
	t := &Token{Format: "cashuA", Entries: v3.Token, Unit: v3.Unit, Memo: v3.Memo}
	return t, t.check()
}

func (t *Token) check() error {
	n := 0
	for _, e := range t.Entries {
		for _, p := range e.Proofs {
			if p.Secret == "" {
				return errors.New("proof without a secret")
			}
			n++
		}
	}
	if n == 0 {
		return errors.New("no proofs")
	}
	return nil
}

// ParseTokenV4 decodes the CBOR body of a cashuB token (NUT-00 TokenV4):
//
//	{"m": mint, "u": unit, "d": memo, "t": [{"i": keyset id bytes,
//	  "p": [{"a": amount, "s": secret, "c": C bytes, "d": dleq, "w": witness}]}]}
func ParseTokenV4(raw []byte) (*Token, error) {
	v, err := decodeCBOR(raw)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("top level is not a map")
	}
	t := &Token{Format: "cashuB"}
	mint, err := optString(m, "m")
	if err != nil {
		return nil, err
	}
	if t.Unit, err = optString(m, "u"); err != nil {
		return nil, err
	}
	if t.Memo, err = optString(m, "d"); err != nil {
		return nil, err
	}
	groups, ok := m["t"].([]any)
	if !ok {
		return nil, errors.New(`missing "t" array`)
	}
	entry := TokenEntry{Mint: mint}
	for gi, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("t[%d] is not a map", gi)
		}
		id, ok := gm["i"].([]byte)
		if !ok {
			return nil, fmt.Errorf(`t[%d]: missing keyset id "i" bytes`, gi)
		}
		ps, ok := gm["p"].([]any)
		if !ok {
			return nil, fmt.Errorf(`t[%d]: missing "p" array`, gi)
		}
		for pi, pv := range ps {
			pm, ok := pv.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("t[%d].p[%d] is not a map", gi, pi)
			}
			p, err := proofV4(pm)
			if err != nil {
				return nil, fmt.Errorf("t[%d].p[%d]: %w", gi, pi, err)
			}
			p.ID = hex.EncodeToString(id)
			entry.Proofs = append(entry.Proofs, p)
		}
	}
	t.Entries = []TokenEntry{entry}
	return t, t.check()
}

func proofV4(m map[string]any) (Proof, error) {
	var p Proof
	a, ok := m["a"].(uint64)
	if !ok {
		return p, errors.New(`missing amount "a"`)
	}
	p.Amount = a
	if p.Secret, ok = m["s"].(string); !ok {
		return p, errors.New(`missing secret "s"`)
	}
	c, ok := m["c"].([]byte)
	if !ok {
		return p, errors.New(`missing signature "c" bytes`)
	}
	p.C = hex.EncodeToString(c)
	if w, ok := m["w"]; ok {
		ws, ok := w.(string)
		if !ok {
			return p, errors.New(`witness "w" is not text`)
		}
		p.Witness, _ = json.Marshal(ws)
	}
	if d, ok := m["d"]; ok {
		dm, ok := d.(map[string]any)
		if !ok {
			return p, errors.New(`dleq "d" is not a map`)
		}
		var dl DLEQ
		for k, dst := range map[string]*string{"e": &dl.E, "s": &dl.S, "r": &dl.R} {
			if b, ok := dm[k].([]byte); ok {
				*dst = hex.EncodeToString(b)
			}
		}
		p.DLEQ = &dl
	}
	return p, nil
}

func optString(m map[string]any, k string) (string, error) {
	v, ok := m[k]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%q is not text", k)
	}
	return s, nil
}

// ParseRaw decodes a raw binary V4 token: "crawB" followed by the CBOR body.
func ParseRaw(b []byte) (*Token, error) {
	if !bytes.HasPrefix(b, []byte("crawB")) {
		return nil, ErrNotToken
	}
	t, err := ParseTokenV4(b[5:])
	if err != nil {
		return nil, fmt.Errorf("crawB token: %w", err)
	}
	return t, nil
}

// ParseInput reads what a user hands to `cashumix checkstate`: a serialized
// token, a raw crawB token, a JSON proof array, {"proofs": [...]}, or a V3 token as JSON.
func ParseInput(data []byte) (*Token, error) {
	if bytes.HasPrefix(data, []byte("crawB")) {
		return ParseRaw(data)
	}
	s := bytes.TrimSpace(data)
	if len(s) == 0 {
		return nil, errors.New("empty input")
	}
	switch s[0] {
	case '[':
		var ps []Proof
		if err := json.Unmarshal(s, &ps); err != nil {
			return nil, fmt.Errorf("proof array: %w", err)
		}
		t := &Token{Format: "proofs", Entries: []TokenEntry{{Proofs: ps}}}
		return t, t.check()
	case '{':
		var obj struct {
			Proofs []Proof      `json:"proofs"`
			Token  []TokenEntry `json:"token"`
			Mint   string       `json:"mint"`
			Unit   string       `json:"unit"`
		}
		if err := json.Unmarshal(s, &obj); err != nil {
			return nil, fmt.Errorf("JSON: %w", err)
		}
		if obj.Token != nil {
			t, err := parseV3JSON(s)
			if t != nil {
				t.Format = "proofs"
			}
			return t, err
		}
		t := &Token{Format: "proofs", Entries: []TokenEntry{{Mint: obj.Mint, Proofs: obj.Proofs}}, Unit: obj.Unit}
		return t, t.check()
	}
	return ParseToken(string(s))
}
