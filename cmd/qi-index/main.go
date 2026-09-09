// Command qi-index runs the Qi ledger indexer.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/leviquai/qi-index/internal/api"
	"github.com/leviquai/qi-index/internal/chain"
	"github.com/leviquai/qi-index/internal/config"
	"github.com/leviquai/qi-index/internal/decoder"
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
	st, closeStore, err := openStore(ctx, cfg.DatabaseURL, log)
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

	if cfg.APIAddr != "" {
		if reader, ok := st.(store.Reader); ok {
			srv := api.New(cfg.APIAddr, reader, log)
			ix.OnUpdate = func(u chain.Update) {
				last := u.Apply[len(u.Apply)-1]
				ev, err := reader.GetBlockUTXOEvents(ctx, last.Height)
				if err != nil {
					log.Warn("api broadcast fetch", "height", last.Height, "err", err)
					return
				}
				srv.Broadcast(ev)
			}
			go func() {
				if err := srv.Start(ctx); err != nil {
					log.Error("api server", "err", err)
				}
			}()
		} else {
			log.Warn("api_addr set but store does not support reads; API disabled")
		}
	}

	return ix.Run(ctx)
}

// openStore returns a Postgres-backed store when dbURL is set, otherwise an
// in-memory one. When Postgres is used the UTXO decoder is injected so UTXO
// events commit atomically with the spine. The returned close func is always
// safe to call.
func openStore(ctx context.Context, dbURL string, log *slog.Logger) (store.Store, func(), error) {
	if dbURL == "" {
		return store.NewMemory(), func() {}, nil
	}
	pg, err := store.NewPostgres(ctx, dbURL)
	if err != nil {
		return nil, nil, err
	}
	pg.SetDecoder(decoder.New(log))
	return pg, pg.Close, nil
}
