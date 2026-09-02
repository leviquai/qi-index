// Command qi-index runs the Qi ledger indexer. M1 scope: `follow` tracks the
// chain spine reorg-safely.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/leviquai/qi-index/internal/config"
	"github.com/leviquai/qi-index/internal/follower"
	"github.com/leviquai/qi-index/internal/indexer"
	"github.com/leviquai/qi-index/internal/rpcclient"
	"github.com/leviquai/qi-index/internal/store"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "follow" {
		fmt.Fprintln(os.Stderr, "usage: qi-index follow [flags]")
		os.Exit(2)
	}

	cfg, err := config.LoadFollow(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}
	log := newLogger(cfg.Verbose)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// run is the composition root: it builds the store, RPC client and follower
// for cfg, then hands them to the indexer to drive.
func run(ctx context.Context, cfg *config.Follow, log *slog.Logger) error {
	st, closeStore, err := openStore(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer closeStore()

	client := rpcclient.New(cfg.RPCURL)

	f, err := follower.New(ctx, st, client, cfg.ReorgDepth, log)
	if err != nil {
		return fmt.Errorf("init follower: %w", err)
	}

	ix := &indexer.Indexer{
		Client:       client,
		Follower:     f,
		Log:          log,
		RPCURL:       cfg.RPCURL,
		WSURL:        cfg.WSURL,
		MetricsAddr:  cfg.MetricsAddr,
		PollInterval: time.Duration(cfg.PollInterval),
		ReorgDepth:   cfg.ReorgDepth,
	}
	return ix.Run(ctx)
}

// openStore returns a Postgres-backed store when dbURL is set, otherwise an
// in-memory one. The returned close func is always safe to call.
func openStore(ctx context.Context, dbURL string) (store.Store, func(), error) {
	if dbURL == "" {
		return store.NewMemory(), func() {}, nil
	}
	pg, err := store.NewPostgres(ctx, dbURL)
	if err != nil {
		return nil, nil, err
	}
	return pg, pg.Close, nil
}
