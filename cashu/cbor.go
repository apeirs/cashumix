package cashu

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// A minimal CBOR (RFC 8949) decoder, enough for TokenV4. It decodes into
// uint64, int64, []byte, string, []any, map[string]any, bool, nil and
// float64, and skips tags. Maps must have text keys, as TokenV4 maps do.

const maxDepth = 32

var errTruncated = errors.New("cbor: truncated input")

type cborDecoder struct {
	b   []byte
	pos int
}

// decodeCBOR decodes exactly one CBOR item from b; trailing bytes are an error.
func decodeCBOR(b []byte) (any, error) {
	d := &cborDecoder{b: b}
	v, err := d.item(0)
	if err != nil {
		return nil, err
	}
	if d.pos != len(b) {
		return nil, fmt.Errorf("cbor: %d trailing bytes", len(b)-d.pos)
	}
	return v, nil
}

func (d *cborDecoder) byte1() (byte, error) {
	if d.pos >= len(d.b) {
		return 0, errTruncated
	}
	c := d.b[d.pos]
	d.pos++
	return c, nil
}

func (d *cborDecoder) take(n uint64) ([]byte, error) {
	if n > uint64(len(d.b)-d.pos) {
		return nil, errTruncated
	}
	out := d.b[d.pos : d.pos+int(n)]
	d.pos += int(n)
	return out, nil
}

// arg reads the argument for additional info ai. indefinite is true for ai 31.
func (d *cborDecoder) arg(ai byte) (n uint64, indefinite bool, err error) {
	switch {
	case ai < 24:
		return uint64(ai), false, nil
	case ai == 24:
		c, err := d.byte1()
		return uint64(c), false, err
	case ai == 25:
		b, err := d.take(2)
		if err != nil {
			return 0, false, err
		}
		return uint64(binary.BigEndian.Uint16(b)), false, nil
	case ai == 26:
		b, err := d.take(4)
		if err != nil {
			return 0, false, err
		}
		return uint64(binary.BigEndian.Uint32(b)), false, nil
	case ai == 27:
		b, err := d.take(8)
		if err != nil {
			return 0, false, err
		}
		return binary.BigEndian.Uint64(b), false, nil
	case ai == 31:
		return 0, true, nil
	}
	return 0, false, fmt.Errorf("cbor: reserved additional info %d", ai)
}

func (d *cborDecoder) isBreak() bool {
	if d.pos < len(d.b) && d.b[d.pos] == 0xff {
		d.pos++
		return true
	}
	return false
}

func (d *cborDecoder) item(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("cbor: nested too deeply")
	}
	ib, err := d.byte1()
	if err != nil {
		return nil, err
	}
	major, ai := ib>>5, ib&0x1f
	if major == 7 {
		return d.simple(ai)
	}
	n, indef, err := d.arg(ai)
	if err != nil {
		return nil, err
	}
	switch major {
	case 0:
		if indef {
			return nil, errors.New("cbor: indefinite integer")
		}
		return n, nil
	case 1:
		if indef || n > math.MaxInt64 {
			return nil, errors.New("cbor: negative integer out of range")
		}
		return -1 - int64(n), nil
	case 2, 3:
		var raw []byte
		if indef {
			for !d.isBreak() {
				c, err := d.item(depth + 1)
				if err != nil {
					return nil, err
				}
				switch c := c.(type) {
				case []byte:
					if major != 2 {
						return nil, errors.New("cbor: bad chunk in indefinite text")
					}
					raw = append(raw, c...)
				case string:
					if major != 3 {
						return nil, errors.New("cbor: bad chunk in indefinite bytes")
					}
					raw = append(raw, c...)
				default:
					return nil, errors.New("cbor: bad chunk in indefinite string")
				}
			}
		} else {
			b, err := d.take(n)
			if err != nil {
				return nil, err
			}
			raw = append([]byte{}, b...)
		}
		if major == 3 {
			return string(raw), nil
		}
		if raw == nil {
			raw = []byte{}
		}
		return raw, nil
	case 4:
		if !indef && n > uint64(len(d.b)-d.pos) {
			return nil, errTruncated // each item takes at least one byte
		}
		var out []any
		for i := uint64(0); indef || i < n; i++ {
			if indef && d.isBreak() {
				break
			}
			v, err := d.item(depth + 1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		if out == nil {
			out = []any{}
		}
		return out, nil
	case 5:
		if !indef && n > uint64(len(d.b)-d.pos)/2 {
			return nil, errTruncated // each pair takes at least two bytes
		}
		out := map[string]any{}
		for i := uint64(0); indef || i < n; i++ {
			if indef && d.isBreak() {
				break
			}
			k, err := d.item(depth + 1)
			if err != nil {
				return nil, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("cbor: map key of type %T (want text)", k)
			}
			v, err := d.item(depth + 1)
			if err != nil {
				return nil, err
			}
			if _, dup := out[ks]; dup {
				return nil, fmt.Errorf("cbor: duplicate map key %q", ks)
			}
			out[ks] = v
		}
		return out, nil
	case 6:
		if indef {
			return nil, errors.New("cbor: indefinite tag")
		}
		return d.item(depth + 1) // tag number ignored
	}
	return nil, fmt.Errorf("cbor: unknown major type %d", major)
}

func (d *cborDecoder) simple(ai byte) (any, error) {
	switch ai {
	case 20:
		return false, nil
	case 21:
		return true, nil
	case 22, 23:
		return nil, nil
	case 25:
		b, err := d.take(2)
		if err != nil {
			return nil, err
		}
		return halfToFloat(binary.BigEndian.Uint16(b)), nil
	case 26:
		b, err := d.take(4)
		if err != nil {
			return nil, err
		}
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), nil
	case 27:
		b, err := d.take(8)
		if err != nil {
			return nil, err
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	case 31:
		return nil, errors.New("cbor: unexpected break")
	}
	return nil, fmt.Errorf("cbor: unsupported simple value %d", ai)
}

func halfToFloat(h uint16) float64 {
	exp := int(h>>10) & 0x1f
	mant := float64(h & 0x3ff)
	var v float64
	switch exp {
	case 0:
		v = math.Ldexp(mant, -24)
	case 31:
		if mant == 0 {
			v = math.Inf(1)
		} else {
			v = math.NaN()
		}
	default:
		v = math.Ldexp(mant+1024, exp-25)
	}
	if h&0x8000 != 0 {
		v = -v
	}
	return v
}
