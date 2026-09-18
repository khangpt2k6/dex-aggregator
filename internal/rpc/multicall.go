package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
)

// Multicall3Address is the same on every chain Multicall3 is deployed to,
// because it was deployed from a pre-signed transaction with a fixed nonce.
const Multicall3Address = "0xcA11bde05977b3631167028862bE2a173976CA11"

// defaultBatchSize is how many contract reads go into one RPC call.
//
// Too small and the rate limit bites; too large and the node rejects the call
// for exceeding its gas cap on eth_call. A hundred reads per batch sits
// comfortably inside every provider limit encountered in practice.
const defaultBatchSize = 100

// Call is one contract read to include in a batch.
type Call struct {
	Target   string
	CallData []byte
}

// Result is the outcome of one call in a batch.
//
// Success is false for a call that reverted. That is not an error: probing an
// address where a pool might exist and finding nothing is the normal way to
// discover which pools are real.
type Result struct {
	Success    bool
	ReturnData []byte
}

// Multicaller batches contract reads through Multicall3.
//
// One refresh wants the state of several hundred pools. Issuing that as several
// hundred eth_call requests is the fastest way to get rate limited off a public
// node. Multicall3 is a contract whose only job is to make one eth_call perform
// many, so the same work costs a handful of requests instead of hundreds.
type Multicaller struct {
	pool      *Pool
	address   string
	batchSize int
}

// NewMulticaller returns a batcher. An empty address uses the canonical
// Multicall3 deployment, and a non-positive batch size uses the default.
func NewMulticaller(p *Pool, address string, batchSize int) *Multicaller {
	if address == "" {
		address = Multicall3Address
	}
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	return &Multicaller{pool: p, address: address, batchSize: batchSize}
}

// Aggregate performs every call, in order, in as few RPC requests as possible.
//
// Results line up one-to-one with calls. A call that reverted comes back with
// Success false rather than failing the whole batch, because one dead pool
// address must not cost us the state of the other ninety-nine.
func (m *Multicaller) Aggregate(ctx context.Context, calls []Call) ([]Result, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	out := make([]Result, 0, len(calls))

	for start := 0; start < len(calls); start += m.batchSize {
		end := start + m.batchSize
		if end > len(calls) {
			end = len(calls)
		}

		chunk := calls[start:end]

		payload, err := encodeTryAggregate(false, chunk)
		if err != nil {
			return nil, err
		}

		raw, err := m.pool.Do(ctx, "eth_call", map[string]string{
			"to":   m.address,
			"data": EncodeHex(payload),
		}, "latest")
		if err != nil {
			return nil, fmt.Errorf("rpc: multicall batch %d-%d: %w", start, end, err)
		}

		var hexResult string
		if err := json.Unmarshal(raw, &hexResult); err != nil {
			return nil, fmt.Errorf("rpc: multicall returned a non-string result: %w", err)
		}

		decoded, err := decodeTryAggregate(hexResult, len(chunk))
		if err != nil {
			return nil, err
		}
		out = append(out, decoded...)
	}

	return out, nil
}

// tryAggregateSignature is Multicall3's forgiving batch entry point: it returns
// per-call success flags rather than reverting the whole batch when one call
// fails.
const tryAggregateSignature = "tryAggregate(bool,(address,bytes)[])"

// encodeTryAggregate builds the call data for tryAggregate.
//
// The argument is a dynamic array of tuples whose second member is itself
// dynamic, so the encoding is three levels of offsets: one to the array, one
// per element into the array's data section, and one inside each tuple to its
// bytes member.
func encodeTryAggregate(requireSuccess bool, calls []Call) ([]byte, error) {
	var (
		head []byte
		body []byte
	)

	// Static head: the bool, then the offset to the array.
	requireWord := make([]byte, wordSize)
	if requireSuccess {
		requireWord[wordSize-1] = 1
	}
	head = append(head, requireWord...)

	// The array starts immediately after the two head words.
	head = append(head, encodeUint64(2*wordSize)...)

	// Array section: length, then one offset per element, then the elements.
	body = append(body, encodeUint64(uint64(len(calls)))...)

	elements := make([][]byte, 0, len(calls))
	for _, c := range calls {
		enc, err := encodeCallTuple(c)
		if err != nil {
			return nil, err
		}
		elements = append(elements, enc)
	}

	// Offsets are relative to the start of the data that follows the length
	// word, so the first element sits after all the offset words.
	offset := uint64(len(calls) * wordSize)
	for _, e := range elements {
		body = append(body, encodeUint64(offset)...)
		offset += uint64(len(e))
	}
	for _, e := range elements {
		body = append(body, e...)
	}

	out := Selector(tryAggregateSignature)
	out = append(out, head...)
	out = append(out, body...)
	return out, nil
}

// encodeCallTuple encodes one (address, bytes) pair.
func encodeCallTuple(c Call) ([]byte, error) {
	addr, err := encodeAddress(c.Target)
	if err != nil {
		return nil, err
	}

	var out []byte
	out = append(out, addr...)

	// The bytes member always starts right after the tuple's two head words.
	out = append(out, encodeUint64(2*wordSize)...)
	out = append(out, encodeUint64(uint64(len(c.CallData)))...)
	out = append(out, padRight(c.CallData)...)
	return out, nil
}

// decodeTryAggregate reads the returned (bool, bytes)[] back out.
func decodeTryAggregate(hexData string, want int) ([]Result, error) {
	data, err := DecodeHex(hexData)
	if err != nil {
		return nil, err
	}
	if len(data) < wordSize*2 {
		return nil, fmt.Errorf("%w: multicall returned %d bytes", ErrShortReturn, len(data))
	}

	// One head word holds the offset to the array.
	arrayOffset, err := decodeUint(data, 0)
	if err != nil {
		return nil, err
	}
	base := int(arrayOffset.Int64())
	if base+wordSize > len(data) {
		return nil, fmt.Errorf("%w: array offset %d past %d bytes", ErrShortReturn, base, len(data))
	}

	count := new(big.Int).SetBytes(data[base : base+wordSize]).Int64()
	if int(count) != want {
		return nil, fmt.Errorf("rpc: multicall returned %d results, sent %d calls", count, want)
	}

	// Element offsets are relative to the first word after the length.
	elementsBase := base + wordSize

	out := make([]Result, 0, want)
	for i := 0; i < want; i++ {
		offWord := elementsBase + i*wordSize
		if offWord+wordSize > len(data) {
			return nil, fmt.Errorf("%w: reading offset for result %d", ErrShortReturn, i)
		}
		rel := int(new(big.Int).SetBytes(data[offWord : offWord+wordSize]).Int64())

		res, err := decodeResultTuple(data, elementsBase+rel)
		if err != nil {
			return nil, fmt.Errorf("rpc: result %d: %w", i, err)
		}
		out = append(out, res)
	}
	return out, nil
}

// decodeResultTuple reads one (bool success, bytes returnData) starting at at.
func decodeResultTuple(data []byte, at int) (Result, error) {
	if at+2*wordSize > len(data) {
		return Result{}, ErrShortReturn
	}

	success := new(big.Int).SetBytes(data[at : at+wordSize]).Sign() != 0

	rel := int(new(big.Int).SetBytes(data[at+wordSize : at+2*wordSize]).Int64())
	lenAt := at + rel
	if lenAt+wordSize > len(data) {
		return Result{}, ErrShortReturn
	}

	size := int(new(big.Int).SetBytes(data[lenAt : lenAt+wordSize]).Int64())
	start := lenAt + wordSize
	if start+size > len(data) {
		return Result{}, ErrShortReturn
	}

	payload := make([]byte, size)
	copy(payload, data[start:start+size])

	return Result{Success: success, ReturnData: payload}, nil
}
