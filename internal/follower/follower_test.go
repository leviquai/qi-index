package follower

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/leviquai/qi-index/internal/chain"
	"github.com/leviquai/qi-index/internal/store"
)

type fakeChain struct {
	blocks map[string]chain.Block
}

func newFakeChain() *fakeChain { return &fakeChain{blocks: make(map[string]chain.Block)} }

func hashOf(branch string, height uint64) string {
	return fmt.Sprintf("0x%s-%06d", branch, height)
}

func (fc *fakeChain) extend(branch, parentBranch string, from, to uint64) chain.Block {
	var last chain.Block
	for h := from + 1; h <= to; h++ {
		parent := hashOf(branch, h-1)
		if h == from+1 {
			parent = hashOf(parentBranch, from)
		}
		b := chain.Block{Height: h, Hash: hashOf(branch, h), ParentHash: parent}
		fc.blocks[b.Hash] = b
		last = b
	}
	return last
}

func (fc *fakeChain) BlockByHash(ctx context.Context, hash string) (chain.Block, error) {
	b, ok := fc.blocks[hash]
	if !ok {
		return chain.Block{}, fmt.Errorf("unknown block %s", hash)
	}
	return b, nil
}

func (fc *fakeChain) get(branch string, height uint64) chain.Block {
	return fc.blocks[hashOf(branch, height)]
}

func newFollower(t *testing.T, st store.Store, fc *fakeChain, depth int) *Follower {
	t.Helper()
	f, err := New(context.Background(), st, fc, depth, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f
}

func feed(t *testing.T, f *Follower, fc *fakeChain, branch string, from, to uint64) {
	t.Helper()
	for h := from + 1; h <= to; h++ {
		if err := f.Ingest(context.Background(), fc.get(branch, h)); err != nil {
			t.Fatalf("ingest %s height %d: %v", branch, h, err)
		}
	}
}

func assertCanonical(t *testing.T, st store.Store, fc *fakeChain, branch string, tipHeight uint64, n int) {
	t.Helper()
	recent, err := st.RecentBlocks(context.Background(), n)
	if err != nil {
		t.Fatalf("RecentBlocks: %v", err)
	}
	h := tipHeight
	for i, b := range recent {
		want := fc.get(branch, h)
		if b.Hash != want.Hash {
			t.Fatalf("canonical mismatch at position %d: got %s want %s", i, b.Hash, want.Hash)
		}
		h--
	}
}

func TestLinearFollow(t *testing.T) {
	fc := newFakeChain()
	fc.extend("main", "main", 100, 150)
	st := store.NewMemory()
	f := newFollower(t, st, fc, 32)

	start := fc.get("main", 101)
	if err := f.Ingest(context.Background(), start); err != nil {
		t.Fatalf("start: %v", err)
	}
	feed(t, f, fc, "main", 101, 150)

	tip, ok, _ := st.Tip(context.Background())
	if !ok || tip.Height != 150 {
		t.Fatalf("tip = %+v, want height 150", tip)
	}
	assertCanonical(t, st, fc, "main", 150, 32)
}

func TestDuplicateIngestIsNoop(t *testing.T) {
	fc := newFakeChain()
	fc.extend("main", "main", 100, 110)
	st := store.NewMemory()
	f := newFollower(t, st, fc, 32)
	f.Ingest(context.Background(), fc.get("main", 101))
	feed(t, f, fc, "main", 101, 110)

	updates := 0
	f.OnUpdate = func(chain.Update) { updates++ }
	if err := f.Ingest(context.Background(), fc.get("main", 105)); err != nil {
		t.Fatalf("dup ingest: %v", err)
	}
	if updates != 0 {
		t.Fatalf("duplicate block produced %d updates", updates)
	}
}

// TestReorgDepths is the M1 acceptance suite: depths 1..20.
func TestReorgDepths(t *testing.T) {
	const tipH = 200
	for depth := uint64(1); depth <= 20; depth++ {
		t.Run(fmt.Sprintf("depth%d", depth), func(t *testing.T) {
			fc := newFakeChain()
			fc.extend("main", "main", 100, tipH)
			forkPoint := tipH - depth
			newTip := fc.extend("fork", "main", forkPoint, tipH+1)

			st := store.NewMemory()
			f := newFollower(t, st, fc, 32)
			f.Ingest(context.Background(), fc.get("main", 101))
			feed(t, f, fc, "main", 101, tipH)

			var got chain.Update
			f.OnUpdate = func(u chain.Update) { got = u }

			if err := f.Ingest(context.Background(), newTip); err != nil {
				t.Fatalf("reorg ingest: %v", err)
			}

			if len(got.Rollback) != int(depth) {
				t.Fatalf("rolled back %d blocks, want %d", len(got.Rollback), depth)
			}
			if len(got.Apply) != int(depth)+1 {
				t.Fatalf("applied %d blocks, want %d", len(got.Apply), depth+1)
			}
			if got.Rollback[0].Height != tipH || got.Rollback[len(got.Rollback)-1].Height != forkPoint+1 {
				t.Fatalf("rollback order wrong: first %d last %d", got.Rollback[0].Height, got.Rollback[len(got.Rollback)-1].Height)
			}
			if got.Apply[0].Height != forkPoint+1 || got.Apply[len(got.Apply)-1].Height != tipH+1 {
				t.Fatalf("apply order wrong: first %d last %d", got.Apply[0].Height, got.Apply[len(got.Apply)-1].Height)
			}
			assertCanonical(t, st, fc, "fork", tipH+1, int(depth)+1)
		})
	}
}

// PoEM can pick a branch that is not taller; the node's head choice wins.
func TestReorgToShorterBranch(t *testing.T) {
	fc := newFakeChain()
	fc.extend("main", "main", 100, 120)
	newTip := fc.extend("fork", "main", 115, 118)

	st := store.NewMemory()
	f := newFollower(t, st, fc, 32)
	f.Ingest(context.Background(), fc.get("main", 101))
	feed(t, f, fc, "main", 101, 120)

	if err := f.Ingest(context.Background(), newTip); err != nil {
		t.Fatalf("shorter-branch reorg: %v", err)
	}
	tip, _, _ := st.Tip(context.Background())
	if tip.Hash != newTip.Hash {
		t.Fatalf("tip = %s, want %s", tip.Hash, newTip.Hash)
	}
	assertCanonical(t, st, fc, "fork", 118, 3)
}

func TestGapFill(t *testing.T) {
	fc := newFakeChain()
	fc.extend("main", "main", 100, 130)
	st := store.NewMemory()
	f := newFollower(t, st, fc, 32)
	f.Ingest(context.Background(), fc.get("main", 101))
	feed(t, f, fc, "main", 101, 110)

	var got chain.Update
	f.OnUpdate = func(u chain.Update) { got = u }
	if err := f.Ingest(context.Background(), fc.get("main", 120)); err != nil {
		t.Fatalf("gap ingest: %v", err)
	}
	if len(got.Rollback) != 0 || len(got.Apply) != 10 {
		t.Fatalf("gap fill: rollback %d apply %d, want 0/10", len(got.Rollback), len(got.Apply))
	}
	assertCanonical(t, st, fc, "main", 120, 20)
}

func (fc *fakeChain) BlocksByRange(ctx context.Context, from, to uint64) ([]chain.Block, error) {
	var out []chain.Block
	for h := from; h < to; h++ {
		if b, ok := fc.blocks[hashOf("main", h)]; ok {
			out = append(out, b)
		}
	}
	return out, nil
}

func TestCatchUpAfterDowntime(t *testing.T) {
	fc := newFakeChain()
	fc.extend("main", "main", 100, 800)
	st := store.NewMemory()
	f := newFollower(t, st, fc, 32)
	f.Ingest(context.Background(), fc.get("main", 101))
	feed(t, f, fc, "main", 101, 110)

	if err := f.CatchUp(context.Background(), 800, fc); err != nil {
		t.Fatalf("catch-up: %v", err)
	}
	tip, _, _ := st.Tip(context.Background())
	if tip.Height != 800 {
		t.Fatalf("tip = %d, want 800", tip.Height)
	}
	assertCanonical(t, st, fc, "main", 800, 32)
}

func TestReorgTooDeep(t *testing.T) {
	const depth = 8
	fc := newFakeChain()
	fc.extend("main", "main", 100, 200)
	newTip := fc.extend("fork", "main", 150, 201)

	st := store.NewMemory()
	f := newFollower(t, st, fc, depth)
	f.Ingest(context.Background(), fc.get("main", 101))
	feed(t, f, fc, "main", 101, 200)

	err := f.Ingest(context.Background(), newTip)
	if !errors.Is(err, ErrReorgTooDeep) {
		t.Fatalf("err = %v, want ErrReorgTooDeep", err)
	}
	tip, _, _ := st.Tip(context.Background())
	if tip.Height != 200 {
		t.Fatalf("store mutated by failed reorg: tip %d", tip.Height)
	}
}

func TestRestartResume(t *testing.T) {
	fc := newFakeChain()
	fc.extend("main", "main", 100, 160)
	newTip := fc.extend("fork", "main", 155, 162)

	st := store.NewMemory()
	f1 := newFollower(t, st, fc, 32)
	f1.Ingest(context.Background(), fc.get("main", 101))
	feed(t, f1, fc, "main", 101, 160)

	f2 := newFollower(t, st, fc, 32)
	if tipHash, tipHeight := f2.Tip(); tipHash != hashOf("main", 160) || tipHeight != 160 {
		t.Fatalf("resumed tip %s/%d, want main-160", tipHash, tipHeight)
	}
	if err := f2.Ingest(context.Background(), newTip); err != nil {
		t.Fatalf("post-restart reorg: %v", err)
	}
	assertCanonical(t, st, fc, "fork", 162, 7)
}

func TestRepeatedReorgFlapping(t *testing.T) {
	fc := newFakeChain()
	fc.extend("main", "main", 100, 120)
	tipB := fc.extend("b", "main", 118, 121)
	tipA2 := fc.extend("a2", "main", 118, 122)

	st := store.NewMemory()
	f := newFollower(t, st, fc, 32)
	f.Ingest(context.Background(), fc.get("main", 101))
	feed(t, f, fc, "main", 101, 120)

	if err := f.Ingest(context.Background(), tipB); err != nil {
		t.Fatalf("flap to b: %v", err)
	}
	if err := f.Ingest(context.Background(), tipA2); err != nil {
		t.Fatalf("flap to a2: %v", err)
	}
	assertCanonical(t, st, fc, "a2", 122, 4)
}
