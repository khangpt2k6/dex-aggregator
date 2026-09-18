package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// HTTPTransport speaks JSON-RPC over HTTP to an Ethereum node.
//
// It deliberately does nothing clever. Concurrency limiting, rate limiting,
// retries and circuit breaking all live in Pool, which wraps this. Keeping the
// transport dumb is what lets the tests drive the interesting behaviour with a
// fake and never touch a network.
type HTTPTransport struct {
	url    string
	client *http.Client
	nextID atomic.Int64
}

// NewHTTPTransport returns a transport pointed at a JSON-RPC endpoint.
func NewHTTPTransport(url string, timeout time.Duration) *HTTPTransport {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &HTTPTransport{
		url: url,
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        64,
				MaxIdleConnsPerHost: 32,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *jsonRPCError   `json:"error"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *jsonRPCError) Error() string {
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

// Call issues one JSON-RPC request.
func (t *HTTPTransport) Call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	if params == nil {
		params = []any{}
	}

	body, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      t.nextID.Add(1),
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return nil, fmt.Errorf("rpc: encoding %s request: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rpc: building %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rpc: %s: %w", method, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	// A 429 is the provider asking for less traffic. Surfacing it as an error
	// lets the pool's breaker and backoff do exactly that.
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("rpc: %s returned HTTP %d: %s", method, resp.StatusCode, bytes.TrimSpace(snippet))
	}

	var out jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("rpc: decoding %s response: %w", method, err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("rpc: %s: %w", method, out.Error)
	}
	return out.Result, nil
}
