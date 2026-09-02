package rpcclient

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/leviquai/qi-index/internal/chain"
)

type spine struct {
	Hash     string `json:"hash"`
	WoHeader struct {
		Hash       string `json:"hash"`
		ParentHash string `json:"parentHash"`
		Number     string `json:"number"`
	} `json:"woHeader"`
}

// ParseBlock extracts spine fields from flat-RPC block JSON. The WS woBody
// shape nests differently and gets its own parser with the decoder.
func ParseBlock(raw json.RawMessage) (chain.Block, error) {
	var s spine
	if err := json.Unmarshal(raw, &s); err != nil {
		return chain.Block{}, fmt.Errorf("parse block spine: %w", err)
	}
	hash := s.Hash
	if hash == "" {
		hash = s.WoHeader.Hash
	}
	if hash == "" || s.WoHeader.ParentHash == "" || s.WoHeader.Number == "" {
		return chain.Block{}, fmt.Errorf("block JSON missing spine fields (hash=%q)", hash)
	}
	height, err := strconv.ParseUint(strings.TrimPrefix(s.WoHeader.Number, "0x"), 16, 64)
	if err != nil {
		return chain.Block{}, fmt.Errorf("parse block number %q: %w", s.WoHeader.Number, err)
	}
	return chain.Block{Height: height, Hash: hash, ParentHash: s.WoHeader.ParentHash, Raw: raw}, nil
}

// BlockByNumber returns found=false when the node has no block at that height.
func (c *Client) BlockByNumber(ctx context.Context, height uint64) (chain.Block, bool, error) {
	raw, err := c.callOne(ctx, "quai_getBlockByNumber", fmt.Sprintf("0x%x", height), true)
	if err != nil {
		return chain.Block{}, false, err
	}
	if string(raw) == "null" {
		return chain.Block{}, false, nil
	}
	b, err := ParseBlock(raw)
	return b, err == nil, err
}

func (c *Client) LatestBlock(ctx context.Context) (chain.Block, error) {
	raw, err := c.callOne(ctx, "quai_getBlockByNumber", "latest", true)
	if err != nil {
		return chain.Block{}, err
	}
	if string(raw) == "null" {
		return chain.Block{}, fmt.Errorf("node returned null for latest block")
	}
	return ParseBlock(raw)
}

func (c *Client) BlockByHash(ctx context.Context, hash string) (chain.Block, error) {
	raw, err := c.callOne(ctx, "quai_getBlockByHash", hash, true)
	if err != nil {
		return chain.Block{}, err
	}
	if string(raw) == "null" {
		return chain.Block{}, fmt.Errorf("block %s not found", hash)
	}
	return ParseBlock(raw)
}

// BlocksByRange fetches [from, to) in one batched HTTP request, skipping
// null heights. Results are height-ordered regardless of response order.
func (c *Client) BlocksByRange(ctx context.Context, from, to uint64) ([]chain.Block, error) {
	if to <= from {
		return nil, nil
	}
	reqs := make([]request, 0, to-from)
	for h := from; h < to; h++ {
		reqs = append(reqs, request{JSONRPC: "2.0", ID: int(h - from), Method: "quai_getBlockByNumber", Params: []any{fmt.Sprintf("0x%x", h), true}})
	}
	res, err := c.call(ctx, reqs)
	if err != nil {
		return nil, err
	}
	blocks := make([]chain.Block, 0, len(res))
	for _, r := range res {
		if r.Error != nil {
			return nil, fmt.Errorf("block %d: %w", from+uint64(r.ID), r.Error)
		}
		if string(r.Result) == "null" {
			continue
		}
		b, err := ParseBlock(r.Result)
		if err != nil {
			return nil, fmt.Errorf("block %d: %w", from+uint64(r.ID), err)
		}
		blocks = append(blocks, b)
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Height < blocks[j].Height })
	return blocks, nil
}
