package cashu

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// A minimal canonical CBOR encoder for TokenV4, writing keys in the order
// the NUT-00 test vectors use, so encoding a decoded vector gives back the
// same bytes.

type cborEncoder struct{ b []byte }

func (e *cborEncoder) head(major byte, n uint64) {
	switch {
	case n < 24:
		e.b = append(e.b, major<<5|byte(n))
	case n <= 0xff:
		e.b = append(e.b, major<<5|24, byte(n))
	case n <= 0xffff:
		e.b = binary.BigEndian.AppendUint16(append(e.b, major<<5|25), uint16(n))
	case n <= 0xffffffff:
		e.b = binary.BigEndian.AppendUint32(append(e.b, major<<5|26), uint32(n))
	default:
		e.b = binary.BigEndian.AppendUint64(append(e.b, major<<5|27), n)
	}
}

func (e *cborEncoder) text(s string)  { e.head(3, uint64(len(s))); e.b = append(e.b, s...) }
func (e *cborEncoder) bytes(b []byte) { e.head(2, uint64(len(b))); e.b = append(e.b, b...) }

func hexBytes(field, s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%s %q is not hex", field, s)
	}
	return b, nil
}

// EncodeV4 serializes the token as "cashuB" + base64url(CBOR), without
// padding. TokenV4 holds one mint, so every entry must name the same one.
func (t *Token) EncodeV4() (string, error) {
	mint := ""
	type group struct {
		id     []byte
		proofs []Proof
	}
	var groups []*group
	byID := map[string]*group{}
	for _, en := range t.Entries {
		if mint != "" && en.Mint != mint {
			return "", errors.New("a cashuB token holds proofs from one mint only")
		}
		mint = en.Mint
		for _, p := range en.Proofs {
			g, ok := byID[p.ID]
			if !ok {
				id, err := hexBytes("keyset id", p.ID)
				if err != nil {
					return "", err
				}
				g = &group{id: id}
				byID[p.ID] = g
				groups = append(groups, g)
			}
			g.proofs = append(g.proofs, p)
		}
	}
	if mint == "" || len(groups) == 0 {
		return "", errors.New("token needs a mint and at least one proof")
	}
	e := &cborEncoder{}
	n := uint64(3)
	if t.Memo != "" {
		n++
	}
	e.head(5, n)
	e.text("t")
	e.head(4, uint64(len(groups)))
	for _, g := range groups {
		e.head(5, 2)
		e.text("i")
		e.bytes(g.id)
		e.text("p")
		e.head(4, uint64(len(g.proofs)))
		for _, p := range g.proofs {
			c, err := hexBytes("C", p.C)
			if err != nil {
				return "", err
			}
			var witness string
			if len(p.Witness) > 0 {
				if json.Unmarshal(p.Witness, &witness) != nil {
					witness = string(p.Witness) // an object: keep its JSON text
				}
			}
			fields := uint64(3)
			if p.DLEQ != nil {
				fields++
			}
			if witness != "" {
				fields++
			}
			e.head(5, fields)
			e.text("a")
			e.head(0, p.Amount)
			e.text("s")
			e.text(p.Secret)
			e.text("c")
			e.bytes(c)
			if p.DLEQ != nil {
				m := map[string]string{"e": p.DLEQ.E, "s": p.DLEQ.S, "r": p.DLEQ.R}
				keys := []string{"e", "s"}
				if p.DLEQ.R != "" {
					keys = append(keys, "r")
				}
				e.text("d")
				e.head(5, uint64(len(keys)))
				for _, k := range keys {
					b, err := hexBytes("dleq "+k, m[k])
					if err != nil {
						return "", err
					}
					e.text(k)
					e.bytes(b)
				}
			}
			if witness != "" {
				e.text("w")
				e.text(witness)
			}
		}
	}
	if t.Memo != "" {
		e.text("d")
		e.text(t.Memo)
	}
	e.text("m")
	e.text(mint)
	unit := t.Unit
	if unit == "" {
		unit = "sat"
	}
	e.text("u")
	e.text(unit)
	return "cashuB" + base64.RawURLEncoding.EncodeToString(e.b), nil
}
