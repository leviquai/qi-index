// Package rpcclient is a minimal batched JSON-RPC client for go-quai zone
// endpoints, returning blocks in the spine shape the follower consumes.
package rpcclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	url  string
	http *http.Client
}

func New(url string) *Client {
	return &Client{url: url, http: &http.Client{Timeout: 60 * time.Second}}
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type response struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

func (c *Client) call(ctx context.Context, reqs []request) ([]response, error) {
	body, err := json.Marshal(reqs)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("http %d: %v", resp.StatusCode, err)
			continue
		}
		var out []response
		if err := json.Unmarshal(data, &out); err != nil {
			var one response // some proxies unwrap single-item batches
			if err2 := json.Unmarshal(data, &one); err2 != nil {
				return nil, fmt.Errorf("decode response: %w", err)
			}
			out = []response{one}
		}
		return out, nil
	}
	return nil, lastErr
}

func (c *Client) callOne(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	res, err := c.call(ctx, []request{{JSONRPC: "2.0", ID: 0, Method: method, Params: params}})
	if err != nil {
		return nil, err
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("%s: empty response", method)
	}
	if res[0].Error != nil {
		return nil, fmt.Errorf("%s: %w", method, res[0].Error)
	}
	return res[0].Result, nil
}

// CallRaw calls a single RPC method with the given params and returns the raw result.
func (c *Client) CallRaw(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	return c.callOne(ctx, method, params...)
}

func (c *Client) BlockNumber(ctx context.Context) (uint64, error) {
	raw, err := c.callOne(ctx, "quai_blockNumber")
	if err != nil {
		return 0, err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
}
