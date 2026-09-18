package rpc

import (
	"context"
	"encoding/json"
	"math/big"
	"sync"
	"testing"
)

// Known selectors, taken from the deployed contracts. Computing them from the
// signature and checking against these turns a typo into a failing test rather
// than a call that returns nothing and is silently treated as a missing pool.
func TestSelectorsMatchKnownValues(t *testing.T) {
	cases := []struct {
		signature string
		want      string
	}{
		{"getReserves()", "0x0902f1ac"},
		{"slot0()", "0x3850c7bd"},
		{"liquidity()", "0x1a686502"},
		{"ticks(int24)", "0xf30dba93"},
		{"tickSpacing()", "0xd0c93a7c"},
		{"token0()", "0x0dfe1681"},
		{"token1()", "0xd21220a7"},
		{"getPool(address,address,uint24)", "0x1698ee82"},
		{"getPair(address,address)", "0xe6a43905"},
		{tryAggregateSignature, "0xbce38bd7"},
	}

	for _, c := range cases {
		if got := EncodeHex(Selector(c.signature)); got != c.want {
			t.Errorf("Selector(%q) = %s, want %s", c.signature, got, c.want)
		}
	}
}

// Ethereum uses original Keccak padding, not the later NIST SHA-3 padding.
// Getting that wrong produces plausible digests that no contract responds to.
func TestKeccakIsLegacyNotSHA3(t *testing.T) {
	// keccak256("") is a well-known constant.
	want := "0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
	if got := EncodeHex(Keccak256(nil)); got != want {
		t.Errorf("Keccak256(empty) = %s, want %s", got, want)
	}
}

func TestDecodeIntHandlesNegatives(t *testing.T) {
	// int24 of -1 is 0xffffff sign-extended across the word.
	data := make([]byte, wordSize)
	for i := range data {
		data[i] = 0xff
	}

	got, err := decodeInt(data, 0, 24)
	if err != nil {
		t.Fatalf("decodeInt: %v", err)
	}
	if got.Int64() != -1 {
		t.Errorf("decodeInt(all ones, int24) = %s, want -1", got)
	}

	// A realistic negative tick: -201600.
	tick := big.NewInt(-201600)
	encoded := new(big.Int).Add(tick, new(big.Int).Lsh(big.NewInt(1), 24))
	buf := padLeft(encoded.Bytes())

	got, err = decodeInt(buf, 0, 24)
	if err != nil {
		t.Fatalf("decodeInt: %v", err)
	}
	if got.Cmp(tick) != 0 {
		t.Errorf("decodeInt = %s, want %s", got, tick)
	}

	// A positive value must survive untouched.
	pos := padLeft(big.NewInt(60).Bytes())
	got, err = decodeInt(pos, 0, 24)
	if err != nil {
		t.Fatalf("decodeInt: %v", err)
	}
	if got.Int64() != 60 {
		t.Errorf("decodeInt(60) = %s, want 60", got)
	}
}

func TestDecodeUintReadsBigValues(t *testing.T) {
	huge, _ := new(big.Int).SetString("1461446703485210103287273052203988822378723970342", 10)
	buf := padLeft(huge.Bytes())

	got, err := decodeUint(buf, 0)
	if err != nil {
		t.Fatalf("decodeUint: %v", err)
	}
	if got.Cmp(huge) != 0 {
		t.Errorf("decodeUint = %s, want %s", got, huge)
	}
}

func TestShortReturnIsDetected(t *testing.T) {
	if _, err := decodeUint(make([]byte, 8), 0); err == nil {
		t.Error("decodeUint on 8 bytes returned nil error, want ErrShortReturn")
	}
	if _, err := word(make([]byte, wordSize), 5); err == nil {
		t.Error("word(5) on one word returned nil error, want ErrShortReturn")
	}
}

// multicallStub behaves the way the deployed contract does: it decodes the
// batch, produces one result per call, and re-encodes them.
type multicallStub struct {
	mu       sync.Mutex
	batches  []int
	failEach func(globalIndex int) bool

	seen int
}

func (m *multicallStub) Call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	if method != "eth_call" {
		return json.RawMessage(`"0x"`), nil
	}

	arg, ok := params[0].(map[string]string)
	if !ok {
		return nil, errBadStubParam
	}

	data, err := DecodeHex(arg["data"])
	if err != nil {
		return nil, err
	}

	count, err := countCalls(data)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.batches = append(m.batches, count)
	base := m.seen
	m.seen += count
	m.mu.Unlock()

	results := make([]Result, 0, count)
	for i := 0; i < count; i++ {
		idx := base + i
		if m.failEach != nil && m.failEach(idx) {
			results = append(results, Result{Success: false})
			continue
		}
		// Echo the global index back so the test can prove ordering.
		results = append(results, Result{Success: true, ReturnData: padLeft(big.NewInt(int64(idx)).Bytes())})
	}

	encoded := encodeResults(results)
	return json.Marshal(EncodeHex(encoded))
}

var errBadStubParam = &stubError{"unexpected eth_call params"}

type stubError struct{ msg string }

func (e *stubError) Error() string { return e.msg }

// countCalls reads the array length out of encoded tryAggregate call data.
func countCalls(data []byte) (int, error) {
	// 4 selector bytes, then the bool, then the array offset.
	body := data[4:]
	off, err := decodeUint(body, 1)
	if err != nil {
		return 0, err
	}
	base := int(off.Int64())
	return int(new(big.Int).SetBytes(body[base : base+wordSize]).Int64()), nil
}

// encodeResults produces what the contract returns for Result[].
func encodeResults(results []Result) []byte {
	var out []byte
	out = append(out, encodeUint64(wordSize)...) // offset to the array
	out = append(out, encodeUint64(uint64(len(results)))...)

	tuples := make([][]byte, 0, len(results))
	for _, r := range results {
		var t []byte

		succ := make([]byte, wordSize)
		if r.Success {
			succ[wordSize-1] = 1
		}
		t = append(t, succ...)
		t = append(t, encodeUint64(2*wordSize)...)
		t = append(t, encodeUint64(uint64(len(r.ReturnData)))...)
		t = append(t, padRight(r.ReturnData)...)

		tuples = append(tuples, t)
	}

	offset := uint64(len(results) * wordSize)
	for _, t := range tuples {
		out = append(out, encodeUint64(offset)...)
		offset += uint64(len(t))
	}
	for _, t := range tuples {
		out = append(out, t...)
	}
	return out
}

func newTestMulticaller(stub Transport, batchSize int) *Multicaller {
	return NewMulticaller(NewPool(stub, fastConfig(4)), Multicall3Address, batchSize)
}

func TestAggregateChunksIntoBatches(t *testing.T) {
	stub := &multicallStub{}
	mc := newTestMulticaller(stub, 100)

	calls := make([]Call, 250)
	for i := range calls {
		calls[i] = Call{Target: Multicall3Address, CallData: Selector("getReserves()")}
	}

	got, err := mc.Aggregate(context.Background(), calls)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}

	if len(got) != 250 {
		t.Fatalf("got %d results, want 250", len(got))
	}
	if len(stub.batches) != 3 {
		t.Errorf("made %d RPC calls, want 3 (100+100+50)", len(stub.batches))
	}
	want := []int{100, 100, 50}
	for i, n := range want {
		if i < len(stub.batches) && stub.batches[i] != n {
			t.Errorf("batch %d had %d calls, want %d", i, stub.batches[i], n)
		}
	}
}

// Results must line up with the calls that produced them, or pool state gets
// attributed to the wrong pool.
func TestAggregatePreservesOrder(t *testing.T) {
	stub := &multicallStub{}
	mc := newTestMulticaller(stub, 10)

	calls := make([]Call, 35)
	for i := range calls {
		calls[i] = Call{Target: Multicall3Address, CallData: Selector("slot0()")}
	}

	got, err := mc.Aggregate(context.Background(), calls)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}

	for i, r := range got {
		if !r.Success {
			t.Errorf("result %d failed unexpectedly", i)
			continue
		}
		n := new(big.Int).SetBytes(r.ReturnData)
		if n.Int64() != int64(i) {
			t.Errorf("result %d carries index %s, want %d", i, n, i)
		}
	}
}

// Probing an address where a pool might not exist is the normal way to discover
// which pools are real, so a reverted sub-call must not fail the batch.
func TestAggregateReportsPerCallFailure(t *testing.T) {
	stub := &multicallStub{failEach: func(i int) bool { return i%3 == 0 }}
	mc := newTestMulticaller(stub, 10)

	calls := make([]Call, 12)
	for i := range calls {
		calls[i] = Call{Target: Multicall3Address, CallData: Selector("token0()")}
	}

	got, err := mc.Aggregate(context.Background(), calls)
	if err != nil {
		t.Fatalf("Aggregate returned an error for a reverted sub-call: %v", err)
	}
	if len(got) != 12 {
		t.Fatalf("got %d results, want 12", len(got))
	}

	for i, r := range got {
		wantFail := i%3 == 0
		if r.Success == wantFail {
			t.Errorf("result %d Success = %v, want %v", i, r.Success, !wantFail)
		}
	}
}

func TestAggregateEmpty(t *testing.T) {
	stub := &multicallStub{}
	mc := newTestMulticaller(stub, 10)

	got, err := mc.Aggregate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Aggregate(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d results, want 0", len(got))
	}
	if len(stub.batches) != 0 {
		t.Errorf("made %d RPC calls for an empty batch, want 0", len(stub.batches))
	}
}

func TestAggregateRejectsBadTarget(t *testing.T) {
	stub := &multicallStub{}
	mc := newTestMulticaller(stub, 10)

	_, err := mc.Aggregate(context.Background(), []Call{{Target: "not-an-address", CallData: []byte{1}}})
	if err == nil {
		t.Error("Aggregate with a malformed target returned nil error, want error")
	}
}

// The encoding is three levels of offsets, so round-tripping it through an
// independent decoder is the only way to be confident it is right.
func TestEncodeTryAggregateRoundTrips(t *testing.T) {
	calls := []Call{
		{Target: "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", CallData: Selector("getReserves()")},
		{Target: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", CallData: append(Selector("ticks(int24)"), padLeft(big.NewInt(60).Bytes())...)},
	}

	encoded, err := encodeTryAggregate(false, calls)
	if err != nil {
		t.Fatalf("encodeTryAggregate: %v", err)
	}

	if got := EncodeHex(encoded[:4]); got != "0xbce38bd7" {
		t.Errorf("selector = %s, want 0xbce38bd7", got)
	}

	n, err := countCalls(encoded)
	if err != nil {
		t.Fatalf("countCalls: %v", err)
	}
	if n != 2 {
		t.Errorf("encoded array length = %d, want 2", n)
	}

	// The whole payload past the selector must be word-aligned, or the node
	// will reject it.
	if len(encoded[4:])%wordSize != 0 {
		t.Errorf("encoded body is %d bytes, not a multiple of %d", len(encoded[4:]), wordSize)
	}
}
