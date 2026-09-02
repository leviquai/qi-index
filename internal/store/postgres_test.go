package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/leviquai/qi-index/internal/chain"
)

func newTestPostgres(t *testing.T) *Postgres {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if !strings.Contains(url, "test") {
		t.Fatalf("TEST_DATABASE_URL must point at a dedicated test database (name containing %q), got %s", "test", url)
	}
	ctx := context.Background()
	p, err := NewPostgres(ctx, url)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	if _, err := p.pool.Exec(ctx, `TRUNCATE blocks, meta`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func blk(branch string, h uint64, parent string) chain.Block {
	return chain.Block{Height: h, Hash: fmt.Sprintf("0x%s-%d", branch, h), ParentHash: parent}
}

func linearChain(branch string, from, to uint64, parent string) []chain.Block {
	var out []chain.Block
	for h := from; h <= to; h++ {
		b := blk(branch, h, parent)
		parent = b.Hash
		out = append(out, b)
	}
	return out
}

func TestPostgresEmptyTip(t *testing.T) {
	p := newTestPostgres(t)
	_, found, err := p.Tip(context.Background())
	if err != nil || found {
		t.Fatalf("Tip on empty store: found=%v err=%v", found, err)
	}
}

func TestPostgresLinearApplyAndResume(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()
	blocks := linearChain("main", 100, 140, "0xgenesis")
	for _, b := range blocks {
		if err := p.ApplyUpdate(ctx, chain.Update{Apply: []chain.Block{b}}); err != nil {
			t.Fatalf("apply %d: %v", b.Height, err)
		}
	}

	tip, found, err := p.Tip(ctx)
	if err != nil || !found || tip.Height != 140 {
		t.Fatalf("tip = %+v found=%v err=%v", tip, found, err)
	}
	recent, err := p.RecentBlocks(ctx, 10)
	if err != nil || len(recent) != 10 {
		t.Fatalf("recent: len=%d err=%v", len(recent), err)
	}
	for i, b := range recent {
		if want := uint64(140 - i); b.Height != want {
			t.Fatalf("recent[%d].Height = %d, want %d", i, b.Height, want)
		}
	}
}

func TestPostgresReorg(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()
	main := linearChain("main", 100, 110, "0xgenesis")
	if err := p.ApplyUpdate(ctx, chain.Update{Apply: main}); err != nil {
		t.Fatalf("apply main: %v", err)
	}

	fork := linearChain("fork", 108, 112, main[7].Hash)
	rollback := []chain.Block{main[10], main[9], main[8]}
	if err := p.ApplyUpdate(ctx, chain.Update{Rollback: rollback, Apply: fork}); err != nil {
		t.Fatalf("reorg: %v", err)
	}

	tip, _, _ := p.Tip(ctx)
	if tip.Hash != fork[len(fork)-1].Hash {
		t.Fatalf("tip = %s, want %s", tip.Hash, fork[len(fork)-1].Hash)
	}
	recent, _ := p.RecentBlocks(ctx, 8)
	if recent[0].Hash != fork[4].Hash || recent[5].Hash != main[7].Hash {
		t.Fatalf("canonical walk wrong after reorg: %v", recent)
	}
	var orphaned bool
	p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blocks WHERE hash=$1)`, main[9].Hash).Scan(&orphaned)
	if orphaned {
		t.Fatalf("rolled-back block %s still present", main[9].Hash)
	}
}

func TestPostgresRejectsDisconnectedUpdate(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()
	main := linearChain("main", 100, 105, "0xgenesis")
	if err := p.ApplyUpdate(ctx, chain.Update{Apply: main}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	stray := blk("stray", 200, "0xnowhere")
	if err := p.ApplyUpdate(ctx, chain.Update{Apply: []chain.Block{stray}}); err == nil {
		t.Fatal("disconnected apply accepted")
	}
	badRollback := chain.Update{
		Rollback: []chain.Block{main[3]},
		Apply:    []chain.Block{blk("x", 104, main[2].Hash)},
	}
	if err := p.ApplyUpdate(ctx, badRollback); err == nil {
		t.Fatal("rollback not starting at tip accepted")
	}

	tip, _, _ := p.Tip(ctx)
	if tip.Hash != main[5].Hash {
		t.Fatalf("failed updates moved tip to %s", tip.Hash)
	}
}
