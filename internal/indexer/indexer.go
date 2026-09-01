// Package indexer runs the follow loop: it feeds new heads from an RPC
// client's WS stream and a safety-net poll into a follower.Follower, records
// Prometheus metrics on every applied update, and serves /metrics.
package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leviquai/qi-index/internal/chain"
	"github.com/leviquai/qi-index/internal/follower"
	"github.com/leviquai/qi-index/internal/metrics"
	"github.com/leviquai/qi-index/internal/rpcclient"
)

// Indexer wires an already-constructed follower and RPC client into the
// follow loop. Callers build its dependencies (store, client, follower) and
// hand them in; Indexer only owns how they're driven.
type Indexer struct {
	Client       *rpcclient.Client
	Follower     *follower.Follower
	Log          *slog.Logger
	RPCURL       string
	WSURL        string
	MetricsAddr  string
	PollInterval time.Duration
	ReorgDepth   int
}

// Run feeds heads into the follower until ctx is canceled. It first catches
// up if the store's tip has fallen behind the chain head, then starts the
// metrics server, the WS subscription (if configured) and the poll loop.
func (ix *Indexer) Run(ctx context.Context) error {
	ix.Follower.OnUpdate = ix.recordUpdate

	if err := ix.catchUpIfBehind(ctx); err != nil {
		return fmt.Errorf("catch-up: %w", err)
	}

	if ix.MetricsAddr != "" {
		go func() {
			if err := metrics.Serve(ix.MetricsAddr); err != nil {
				ix.Log.Error("metrics server", "err", err)
			}
		}()
	}

	heads := make(chan chain.Block, 16)
	if ix.WSURL != "" {
		go rpcclient.StreamBlocks(ctx, ix.WSURL, heads, ix.Log, func() { metrics.WSReconnects.Inc() })
	}

	ix.Log.Info("following", "rpc", ix.RPCURL, "ws", ix.WSURL, "poll", ix.PollInterval, "depth", ix.ReorgDepth)
	ticker := time.NewTicker(ix.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			ix.Log.Info("shutting down")
			return nil
		case head := <-heads:
			ix.ingest(ctx, "ws", head)
		case <-ticker.C:
			head, err := ix.Client.LatestBlock(ctx)
			if err != nil {
				metrics.RPCErrors.Inc()
				ix.Log.Warn("fetch head", "err", err)
				continue
			}
			ix.ingest(ctx, "poll", head)
		}
	}
}

func (ix *Indexer) recordUpdate(u chain.Update) {
	metrics.BlocksApplied.Add(float64(len(u.Apply)))
	if len(u.Rollback) > 0 {
		metrics.Reorgs.Inc()
		metrics.ReorgDepth.Observe(float64(len(u.Rollback)))
	}
	metrics.TipHeight.Set(float64(u.Apply[len(u.Apply)-1].Height))
}

// catchUpIfBehind resyncs from a checkpoint when the store's tip is more
// than ReorgDepth blocks behind the current chain head, e.g. after downtime.
func (ix *Indexer) catchUpIfBehind(ctx context.Context) error {
	_, tipHeight := ix.Follower.Tip()
	if tipHeight == 0 {
		return nil
	}
	head, err := ix.Client.LatestBlock(ctx)
	if err != nil || head.Height <= tipHeight+uint64(ix.ReorgDepth) {
		return nil
	}
	return ix.Follower.CatchUp(ctx, head.Height, ix.Client)
}

func (ix *Indexer) ingest(ctx context.Context, source string, head chain.Block) {
	err := ix.Follower.Ingest(ctx, head)
	if _, tipHeight := ix.Follower.Tip(); errors.Is(err, follower.ErrReorgTooDeep) && head.Height > tipHeight+uint64(ix.ReorgDepth) {
		err = ix.Follower.CatchUp(ctx, head.Height, ix.Client)
	}
	if err != nil {
		metrics.RPCErrors.Inc()
		ix.Log.Error("ingest", "source", source, "height", head.Height, "err", err)
	}
}
