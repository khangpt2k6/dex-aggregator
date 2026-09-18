// Package onchain reads real pool state from an Ethereum node.
//
// Pool addresses are derived rather than discovered. Both factories create
// pools with CREATE2, which means a pool's address is a pure function of the
// factory, the token pair, and the fee tier. Computing the address locally and
// then probing it costs nothing, whereas asking the factory for every possible
// pair would be one RPC round trip per pair before any state is read at all.
//
// Probing an address that holds no pool is not an error. It is how the package
// learns which pools exist.
package onchain

import (
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/khangpt2k6/dex-aggregator/internal/dex"
	"github.com/khangpt2k6/dex-aggregator/internal/rpc"
)

// Mainnet factory deployments and their pool init code hashes. The init code
// hash is fixed at deployment and is what makes CREATE2 address derivation
// possible without touching the chain.
const (
	UniswapV3Factory      = "0x1F98431c8aD98523631AE4a59f267346ea31F984"
	uniswapV3InitCodeHash = "0xe34f199b19b2b4f47f68442619d555527d244f78a3297ea89325f843f87b8b54"

	SushiswapV2Factory      = "0xC0AEe478e3658e2610c5F7A4A2E1777cE9e4f2Ac"
	sushiswapV2InitCodeHash = "0xe18a34eb0e04b04f7a0ac29a6e80748dca96319b42c54d679cb821dca90c6303"
)

// DefaultV3FeeTiers are the fee tiers worth probing, in basis points.
//
// Uniswap expresses these in hundredths of a bip (100, 500, 3000, 10000), which
// is 1, 5, 30 and 100 basis points. The 1 bps tier exists but carries so little
// liquidity outside a few stable pairs that probing it costs more than it
// returns.
var DefaultV3FeeTiers = []uint32{5, 30, 100}

// orderTokens sorts a pair the way both factories do, by address ascending.
// The salt depends on this order, so getting it wrong derives an address that
// holds nothing.
func orderTokens(a, b dex.Token) (dex.Token, dex.Token) {
	if strings.ToLower(a.Address) > strings.ToLower(b.Address) {
		return b, a
	}
	return a, b
}

// create2 computes the address a CREATE2 deployment lands at:
//
//	keccak256(0xff ++ factory ++ salt ++ initCodeHash)[12:]
func create2(factory string, salt, initCodeHash []byte) (string, error) {
	f, err := rpc.DecodeHex(factory)
	if err != nil {
		return "", err
	}
	if len(f) != 20 {
		return "", fmt.Errorf("onchain: factory %q is not a 20-byte address", factory)
	}

	digest := rpc.Keccak256([]byte{0xff}, f, salt, initCodeHash)
	return rpc.EncodeHex(digest[12:]), nil
}

// v3PoolAddress derives a Uniswap V3 pool address.
//
// The salt is keccak256(abi.encode(token0, token1, fee)), where fee is in
// hundredths of a bip, so the basis-point fee is scaled by 100 here.
func v3PoolAddress(a, b dex.Token, feeBps uint32) (string, error) {
	t0, t1 := orderTokens(a, b)

	addr0, err := rpc.DecodeHex(t0.Address)
	if err != nil {
		return "", err
	}
	addr1, err := rpc.DecodeHex(t1.Address)
	if err != nil {
		return "", err
	}

	var packed []byte
	packed = append(packed, leftPad32(addr0)...)
	packed = append(packed, leftPad32(addr1)...)
	packed = append(packed, leftPad32(new(big.Int).SetUint64(uint64(feeBps)*100).Bytes())...)

	salt := rpc.Keccak256(packed)

	hash, err := rpc.DecodeHex(uniswapV3InitCodeHash)
	if err != nil {
		return "", err
	}
	return create2(UniswapV3Factory, salt, hash)
}

// v2PairAddress derives a Sushiswap V2 pair address.
//
// The V2 salt uses abi.encodePacked, so the two addresses are concatenated at
// their natural 20 bytes rather than padded to words. Padding them the way V3
// does would derive a completely different, empty address.
func v2PairAddress(a, b dex.Token) (string, error) {
	t0, t1 := orderTokens(a, b)

	addr0, err := rpc.DecodeHex(t0.Address)
	if err != nil {
		return "", err
	}
	addr1, err := rpc.DecodeHex(t1.Address)
	if err != nil {
		return "", err
	}

	salt := rpc.Keccak256(addr0, addr1)

	hash, err := rpc.DecodeHex(sushiswapV2InitCodeHash)
	if err != nil {
		return "", err
	}
	return create2(SushiswapV2Factory, salt, hash)
}

func leftPad32(b []byte) []byte {
	if len(b) >= 32 {
		return b
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

// pairsOf returns every unordered pair of the given tokens, in a stable order
// so that a refresh issues its calls in the same sequence every time.
func pairsOf(tokens []dex.Token) [][2]dex.Token {
	sorted := make([]dex.Token, len(tokens))
	copy(sorted, tokens)
	sort.Slice(sorted, func(i, j int) bool {
		return strings.ToLower(sorted[i].Address) < strings.ToLower(sorted[j].Address)
	})

	var out [][2]dex.Token
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			out = append(out, [2]dex.Token{sorted[i], sorted[j]})
		}
	}
	return out
}
