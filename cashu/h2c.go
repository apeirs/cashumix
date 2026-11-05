// Package cashu holds the small part of the Cashu protocol cashumix needs:
// NUT-00 hash_to_curve, token decoding (cashuA / cashuB), and a client for
// the read-only mint endpoints (NUT-06 info, NUT-02 keysets, NUT-07 state).
//
// It never signs, blinds, mints or spends anything. Wallets keep doing that;
// cashumix only moves their requests through the mixnet.
package cashu

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// DomainSeparator is the NUT-00 hash_to_curve domain separator.
var DomainSeparator = []byte("Secp256k1_HashToCurve_Cashu_")

// ErrNoPoint is returned if no counter below 2^16 gives a curve point. For
// SHA-256 output this does not happen in practice (each try succeeds with
// probability about 1/2).
var ErrNoPoint = errors.New("hash_to_curve: no valid point found")

// HashToCurve maps msg to a secp256k1 point exactly as NUT-00 specifies:
//
//	msg_hash = SHA256(DOMAIN_SEPARATOR || msg)
//	Y = PublicKey('02' || SHA256(msg_hash || counter))
//
// where counter is a little-endian uint32 counting up from 0 until the
// x-coordinate lies on the curve.
func HashToCurve(msg []byte) (*secp256k1.PublicKey, error) {
	msgHash := sha256.Sum256(append(append([]byte{}, DomainSeparator...), msg...))
	var buf [36]byte
	copy(buf[:32], msgHash[:])
	var cand [33]byte
	cand[0] = 0x02
	for counter := uint32(0); counter < 1<<16; counter++ {
		binary.LittleEndian.PutUint32(buf[32:], counter)
		x := sha256.Sum256(buf[:])
		copy(cand[1:], x[:])
		if pk, err := secp256k1.ParsePubKey(cand[:]); err == nil {
			return pk, nil
		}
	}
	return nil, ErrNoPoint
}

// Y is the NUT-07 identifier of a proof: the compressed point
// hash_to_curve(secret) in hex, where secret is the proof's secret string
// taken as UTF-8 bytes (not hex-decoded).
func Y(secret string) (string, error) {
	pk, err := HashToCurve([]byte(secret))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(pk.SerializeCompressed()), nil
}
