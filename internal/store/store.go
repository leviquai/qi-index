// Package store defines the persistence interface the follower writes through.
// Implementations must make ApplyUpdate atomic: the rollback, the apply and
// the tip move in one transaction, so a crash can never leave the stored tip
// pointing at data that was not written (the electrs tip-in-batch property).
package store

import (
	"context"

	"github.com/leviquai/qi-index/internal/chain"
)

type Store interface {
	// Tip returns the current canonical tip, or found=false on an empty store.
	Tip(ctx context.Context) (tip chain.Block, found bool, err error)
	// RecentBlocks returns up to n canonical blocks ordered tip-first,
	// used to rebuild the follower's in-memory window after a restart.
	RecentBlocks(ctx context.Context, n int) ([]chain.Block, error)
	// ApplyUpdate atomically removes the rollback blocks, inserts the apply
	// blocks and moves the tip to the last applied block.
	ApplyUpdate(ctx context.Context, u chain.Update) error
}
