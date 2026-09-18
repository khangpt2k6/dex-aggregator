package dex

import "strings"

// registry holds the tokens the aggregator routes through.
//
// These are real Ethereum mainnet addresses, checksummed. They are used as-is
// for live RPC reads and as labels for simulated pools, so a route printed in
// simulated mode names the same assets it would name against a real node.
//
// The set is deliberately small. Every extra token multiplies the number of
// candidate pools, and beyond the major pairs the added liquidity does not
// change routing outcomes enough to pay for the RPC cost.
var registry = []Token{
	{Address: "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", Symbol: "WETH", Decimals: 18},
	{Address: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", Symbol: "USDC", Decimals: 6},
	{Address: "0xdAC17F958D2ee523a2206206994597C13D831ec7", Symbol: "USDT", Decimals: 6},
	{Address: "0x6B175474E89094C44Da98b954EedeAC495271d0F", Symbol: "DAI", Decimals: 18},
	{Address: "0x2260FAC5E5542a773Aa44fBCfeDf7C193bc2C599", Symbol: "WBTC", Decimals: 8},
	{Address: "0x514910771AF9Ca656af840dff83E8264EcF986CA", Symbol: "LINK", Decimals: 18},
	{Address: "0x1f9840a85d5aF5bf1D1762F925BDADdC4201F984", Symbol: "UNI", Decimals: 18},
	{Address: "0x7Fc66500c84A76Ad7e9c93437bFc5Ac33E2DDaE9", Symbol: "AAVE", Decimals: 18},
}

// Tokens returns the token registry.
//
// The returned slice is a copy. The registry is read by every request handler
// and by the indexer, so handing out the backing array would make one careless
// caller able to corrupt routing for the whole process.
func Tokens() []Token {
	out := make([]Token, len(registry))
	copy(out, registry)
	return out
}

// TokenBySymbol looks up a token by ticker, ignoring case.
func TokenBySymbol(symbol string) (Token, bool) {
	want := strings.TrimSpace(symbol)
	for _, tok := range registry {
		if strings.EqualFold(tok.Symbol, want) {
			return tok, true
		}
	}
	return Token{}, false
}

// TokenByAddress looks up a token by contract address, ignoring case.
func TokenByAddress(address string) (Token, bool) {
	for _, tok := range registry {
		if addrEqual(tok.Address, address) {
			return tok, true
		}
	}
	return Token{}, false
}

// Resolve accepts either a symbol or a contract address, so API callers can
// use whichever they have without a separate lookup endpoint.
func Resolve(ref string) (Token, bool) {
	if tok, ok := TokenBySymbol(ref); ok {
		return tok, true
	}
	return TokenByAddress(ref)
}
