// Package follower maintains a reorg-safe view of one zone's chain spine: an
// in-memory window of recent headers, a parent-hash check per incoming block,
// and on divergence a walk to the common ancestor producing one atomic
// chain.Update. The node's head choice is trusted (PoEM), even when the new
// branch is not taller.
package follower

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leviquai/qi-index/internal/chain"
	"github.com/leviquai/qi-index/internal/store"
)

// ErrReorgTooDeep means no common ancestor was found within the window;
// resync from a checkpoint.
var ErrReorgTooDeep = errors.New("reorg deeper than follower window")

// Fetcher retrieves blocks by hash for ancestor walks during reorgs and gaps.
type Fetcher interface {
	BlockByHash(ctx context.Context, hash string) (chain.Block, error)
}

// RangeFetcher retrieves blocks by height range for catch-up after downtime.
type RangeFetcher interface {
	BlocksByRange(ctx context.Context, from, to uint64) ([]chain.Block, error)
}

type Follower struct {
	store    store.Store
	fetch    Fetcher
	log      *slog.Logger
	maxDepth int

	window    map[string]chain.Block
	tip       string
	tipHeight uint64

	// OnUpdate, if set, is called after every committed update.
	OnUpdate func(chain.Update)
}

// New rebuilds the window from the store so a restarted process resumes where
// the last commit ended.
func New(ctx context.Context, st store.Store, fetch Fetcher, maxDepth int, log *slog.Logger) (*Follower, error) {
	if log == nil {
		log = slog.Default()
	}
	f := &Follower{
		store:    st,
		fetch:    fetch,
		log:      log,
		maxDepth: maxDepth,
		window:   make(map[string]chain.Block),
	}
	recent, err := st.RecentBlocks(ctx, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("rebuild window: %w", err)
	}
	for _, b := range recent {
		f.window[b.Hash] = b
	}
	if len(recent) > 0 {
		f.tip = recent[0].Hash
		f.tipHeight = recent[0].Height
		log.Info("follower resumed", "tip", f.tip, "height", f.tipHeight, "window", len(recent))
	}
	return f, nil
}

// Tip returns the current tip hash and height ("" and 0 before first block).
func (f *Follower) Tip() (string, uint64) { return f.tip, f.tipHeight }

// Ingest processes one block the node reports as head, committing exactly one
// atomic store update. Duplicates are no-ops; gaps are filled via the Fetcher.
func (f *Follower) Ingest(ctx context.Context, b chain.Block) error {
	if _, known := f.window[b.Hash]; known {
		return nil
	}

	var update chain.Update
	switch {
	case f.tip == "" || b.ParentHash == f.tip:
		update = chain.Update{Apply: []chain.Block{b}}
	default:
		branch, ancestor, err := f.walkToKnown(ctx, b)
		if err != nil {
			return err
		}
		rollback, err := f.rollbackFrom(ancestor)
		if err != nil {
			return err
		}
		update = chain.Update{Rollback: rollback, Apply: branch}
	}

	if err := f.store.ApplyUpdate(ctx, update); err != nil {
		return fmt.Errorf("commit update at %d: %w", b.Height, err)
	}

	for _, rb := range update.Rollback {
		delete(f.window, rb.Hash)
	}
	for _, ab := range update.Apply {
		f.window[ab.Hash] = ab
	}
	f.tip = b.Hash
	f.tipHeight = b.Height
	f.prune()

	if len(update.Rollback) > 0 {
		f.log.Warn("reorg",
			"depth", len(update.Rollback), "applied", len(update.Apply),
			"new_tip", b.Hash, "height", b.Height)
	} else {
		f.log.Debug("block applied", "hash", b.Hash, "height", b.Height, "count", len(update.Apply))
	}
	if f.OnUpdate != nil {
		f.OnUpdate(update)
	}
	return nil
}

// walkToKnown fetches b's ancestry until it connects to the window, returning
// the branch ordered ancestor→tip and the known ancestor's hash.
func (f *Follower) walkToKnown(ctx context.Context, b chain.Block) ([]chain.Block, string, error) {
	branch := []chain.Block{b}
	parent := b.ParentHash
	for {
		if _, known := f.window[parent]; known {
			return branch, parent, nil
		}
		if len(branch) >= f.maxDepth {
			return nil, "", fmt.Errorf("%w: no common ancestor within %d blocks of %s", ErrReorgTooDeep, f.maxDepth, b.Hash)
		}
		pb, err := f.fetch.BlockByHash(ctx, parent)
		if err != nil {
			return nil, "", fmt.Errorf("fetch ancestor %s: %w", parent, err)
		}
		branch = append([]chain.Block{pb}, branch...)
		parent = pb.ParentHash
	}
}

// rollbackFrom collects canonical blocks from tip down to (excluding)
// ancestor, ordered tip→ancestor.
func (f *Follower) rollbackFrom(ancestor string) ([]chain.Block, error) {
	var rollback []chain.Block
	cur := f.tip
	for cur != ancestor {
		blk, ok := f.window[cur]
		if !ok {
			return nil, fmt.Errorf("window broken: %s not found walking to ancestor %s", cur, ancestor)
		}
		rollback = append(rollback, blk)
		cur = blk.ParentHash
	}
	return rollback, nil
}

// CatchUp advances the tip to targetHeight for gaps wider than the window.
func (f *Follower) CatchUp(ctx context.Context, targetHeight uint64, rf RangeFetcher) error {
	const batch = 100
	f.log.Info("catching up", "from", f.tipHeight, "to", targetHeight)
	for f.tipHeight < targetHeight {
		from := f.tipHeight + 1
		to := min(from+batch, targetHeight+1)
		blocks, err := rf.BlocksByRange(ctx, from, to)
		if err != nil {
			return fmt.Errorf("catch-up fetch [%d,%d): %w", from, to, err)
		}
		if len(blocks) == 0 {
			return nil
		}
		for _, b := range blocks {
			if err := f.Ingest(ctx, b); err != nil {
				return fmt.Errorf("catch-up ingest %d: %w", b.Height, err)
			}
		}
	}
	return nil
}

func (f *Follower) prune() {
	if f.tipHeight < uint64(f.maxDepth) {
		return
	}
	floor := f.tipHeight - uint64(f.maxDepth)
	for h, blk := range f.window {
		if blk.Height < floor {
			delete(f.window, h)
		}
	}
}
