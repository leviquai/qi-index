// Command qi-index runs the Qi ledger indexer. M1 scope: `follow` tracks the
// chain spine reorg-safely.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/leviquai/qi-index/internal/chain"
	"github.com/leviquai/qi-index/internal/follower"
	"github.com/leviquai/qi-index/internal/metrics"
	"github.com/leviquai/qi-index/internal/rpcclient"
	"github.com/leviquai/qi-index/internal/store"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "follow" {
		fmt.Fprintln(os.Stderr, "usage: qi-index follow [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("follow", flag.ExitOnError)
	rpcURL := fs.String("rpc", "https://rpc.quai.network/cyprus1", "zone RPC URL")
	wsURL := fs.String("ws", "wss://rpc.quai.network/cyprus1", "zone WS URL (empty = poll only)")
	dbURL := fs.String("db", "", "Postgres URL (empty = in-memory store)")
	poll := fs.Duration("poll", 15*time.Second, "safety-net head poll interval")
	depth := fs.Int("depth", 32, "reorg window depth")
	metricsAddr := fs.String("metrics", "", "metrics listen address, e.g. :2112 (empty = off)")
	verbose := fs.Bool("v", false, "debug logging")
	fs.Parse(os.Args[2:])

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := rpcclient.New(*rpcURL)
	var st store.Store = store.NewMemory()
	if *dbURL != "" {
		pg, err := store.NewPostgres(ctx, *dbURL)
		if err != nil {
			log.Error("connect postgres", "err", err)
			os.Exit(1)
		}
		defer pg.Close()
		st = pg
	}
	f, err := follower.New(ctx, st, client, *depth, log)
	if err != nil {
		log.Error("init follower", "err", err)
		os.Exit(1)
	}
	f.OnUpdate = func(u chain.Update) {
		metrics.BlocksApplied.Add(float64(len(u.Apply)))
		if len(u.Rollback) > 0 {
			metrics.Reorgs.Inc()
			metrics.ReorgDepth.Observe(float64(len(u.Rollback)))
		}
		metrics.TipHeight.Set(float64(u.Apply[len(u.Apply)-1].Height))
	}

	if _, tipHeight := f.Tip(); tipHeight > 0 {
		if head, err := client.LatestBlock(ctx); err == nil && head.Height > tipHeight+uint64(*depth) {
			if err := f.CatchUp(ctx, head.Height, client); err != nil {
				log.Error("catch-up", "err", err)
				os.Exit(1)
			}
		}
	}

	if *metricsAddr != "" {
		go func() {
			if err := metrics.Serve(*metricsAddr); err != nil {
				log.Error("metrics server", "err", err)
			}
		}()
	}

	heads := make(chan chain.Block, 16)
	if *wsURL != "" {
		go rpcclient.StreamBlocks(ctx, *wsURL, heads, log, func() { metrics.WSReconnects.Inc() })
	}

	log.Info("following", "rpc", *rpcURL, "ws", *wsURL, "poll", *poll, "depth", *depth)
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case head := <-heads:
			if err := ingestOrCatchUp(ctx, f, client, *depth, head); err != nil {
				metrics.RPCErrors.Inc()
				log.Error("ingest ws head", "height", head.Height, "err", err)
			}
		case <-ticker.C:
			head, err := client.LatestBlock(ctx)
			if err != nil {
				metrics.RPCErrors.Inc()
				log.Warn("fetch head", "err", err)
				continue
			}
			if err := ingestOrCatchUp(ctx, f, client, *depth, head); err != nil {
				metrics.RPCErrors.Inc()
				log.Error("ingest polled head", "height", head.Height, "err", err)
			}
		}
	}
}

func ingestOrCatchUp(ctx context.Context, f *follower.Follower, client *rpcclient.Client, depth int, head chain.Block) error {
	err := f.Ingest(ctx, head)
	if _, tipHeight := f.Tip(); errors.Is(err, follower.ErrReorgTooDeep) && head.Height > tipHeight+uint64(depth) {
		return f.CatchUp(ctx, head.Height, client)
	}
	return err
}
