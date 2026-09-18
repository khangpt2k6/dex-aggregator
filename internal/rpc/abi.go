package rpc

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/sha3"
)

// wordSize is the 32-byte slot every ABI value is padded to.
const wordSize = 32

// ErrShortReturn is returned when a contract call returned fewer bytes than the
// signature promises, which usually means the address is not the contract we
// think it is.
var ErrShortReturn = errors.New("rpc: contract return data is shorter than expected")

// Keccak256 returns the Keccak-256 digest.
//
// Ethereum uses the original Keccak padding rather than the later
// NIST-standardised SHA-3 padding. They produce different digests, so
// sha3.New256 here would compute plausible-looking selectors that no contract
// on earth responds to.
func Keccak256(data ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, d := range data {
		h.Write(d)
	}
	return h.Sum(nil)
}

// Selector returns the four-byte function selector for a signature such as
// "getReserves()".
//
// Computing these rather than pasting hex constants means a typo becomes a
// failing test against a known value instead of a call that silently returns
// nothing.
func Selector(signature string) []byte {
	return Keccak256([]byte(signature))[:4]
}

// padLeft left-pads b to a full 32-byte word, which is how the ABI encodes
// numbers and addresses.
func padLeft(b []byte) []byte {
	if len(b) >= wordSize {
		return b
	}
	out := make([]byte, wordSize)
	copy(out[wordSize-len(b):], b)
	return out
}

// padRight right-pads b to a multiple of 32 bytes, which is how the ABI encodes
// raw byte strings.
func padRight(b []byte) []byte {
	if len(b)%wordSize == 0 {
		return b
	}
	out := make([]byte, ((len(b)/wordSize)+1)*wordSize)
	copy(out, b)
	return out
}

// encodeUint encodes an unsigned integer as one word.
func encodeUint(n *big.Int) []byte {
	return padLeft(n.Bytes())
}

// encodeUint64 encodes a small unsigned integer as one word.
func encodeUint64(n uint64) []byte {
	return encodeUint(new(big.Int).SetUint64(n))
}

// encodeAddress encodes a hex address as one left-padded word.
func encodeAddress(addr string) ([]byte, error) {
	raw, err := DecodeHex(addr)
	if err != nil {
		return nil, err
	}
	if len(raw) != 20 {
		return nil, fmt.Errorf("rpc: %q is not a 20-byte address", addr)
	}
	return padLeft(raw), nil
}

// DecodeHex parses a hex string with or without the 0x prefix.
func DecodeHex(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")

	// Hex from a node is occasionally odd-length when leading zeroes are
	// trimmed, which hex.DecodeString rejects outright.
	if len(s)%2 == 1 {
		s = "0" + s
	}

	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("rpc: decoding hex %q: %w", s, err)
	}
	return out, nil
}

// EncodeHex renders bytes as an 0x-prefixed hex string.
func EncodeHex(b []byte) string {
	return "0x" + hex.EncodeToString(b)
}

// word returns the i-th 32-byte word of return data.
func word(data []byte, i int) ([]byte, error) {
	start := i * wordSize
	if len(data) < start+wordSize {
		return nil, fmt.Errorf("%w: wanted word %d of %d bytes", ErrShortReturn, i, len(data))
	}
	return data[start : start+wordSize], nil
}

// decodeUint reads the i-th word as an unsigned integer.
func decodeUint(data []byte, i int) (*big.Int, error) {
	w, err := word(data, i)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(w), nil
}

// decodeInt reads the i-th word as a two's complement signed integer of the
// given bit width.
//
// Ticks are int24 and liquidityNet is int128, both of which are routinely
// negative. Reading them as unsigned would turn a small negative tick into an
// astronomically large positive one, and the pool would quote nonsense.
func decodeInt(data []byte, i int, bits uint) (*big.Int, error) {
	w, err := word(data, i)
	if err != nil {
		return nil, err
	}

	n := new(big.Int).SetBytes(w)

	// The ABI sign-extends a narrow signed type across the whole word, so a
	// tick of -1 arrives as thirty-two bytes of 0xff. Mask down to the declared
	// width first; reading the raw word would turn -1 into 2^256-1.
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), bits), big.NewInt(1))
	n.And(n, mask)

	// If the sign bit is set, subtract 2^bits to recover the negative value.
	signBit := new(big.Int).Lsh(big.NewInt(1), bits-1)
	if n.Cmp(signBit) >= 0 {
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), bits))
	}
	return n, nil
}

// decodeAddress reads the i-th word as an address.
func decodeAddress(data []byte, i int) (string, error) {
	w, err := word(data, i)
	if err != nil {
		return "", err
	}
	return EncodeHex(w[12:]), nil
}
