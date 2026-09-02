package store

import (
	"context"
	"fmt"
	"sync"

	"github.com/leviquai/qi-index/internal/chain"
)

// Memory is an in-process Store for tests and database-less runs. It enforces
// the same connectivity rules Postgres must.
type Memory struct {
	mu     sync.Mutex
	blocks map[string]chain.Block
	tip    string
}

func NewMemory() *Memory {
	return &Memory{blocks: make(map[string]chain.Block)}
}

func (m *Memory) Tip(ctx context.Context) (chain.Block, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tip == "" {
		return chain.Block{}, false, nil
	}
	return m.blocks[m.tip], true, nil
}

func (m *Memory) RecentBlocks(ctx context.Context, n int) ([]chain.Block, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]chain.Block, 0, n)
	cur := m.tip
	for len(out) < n && cur != "" {
		b, ok := m.blocks[cur]
		if !ok {
			break
		}
		out = append(out, b)
		cur = b.ParentHash
	}
	return out, nil
}

func (m *Memory) ApplyUpdate(ctx context.Context, u chain.Update) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Validate everything before mutating so a bad update leaves no partial state.
	cur := m.tip
	for _, rb := range u.Rollback {
		if rb.Hash != cur {
			return fmt.Errorf("rollback block %s is not the current tip %s", rb.Hash, cur)
		}
		cur = rb.ParentHash
	}
	if len(u.Apply) == 0 {
		return fmt.Errorf("update applies no blocks")
	}
	if m.tip != "" && u.Apply[0].ParentHash != cur {
		return fmt.Errorf("apply block %s does not connect to %s", u.Apply[0].Hash, cur)
	}
	for i := 1; i < len(u.Apply); i++ {
		if u.Apply[i].ParentHash != u.Apply[i-1].Hash {
			return fmt.Errorf("apply blocks not contiguous at %s", u.Apply[i].Hash)
		}
	}

	for _, rb := range u.Rollback {
		delete(m.blocks, rb.Hash)
	}
	for _, b := range u.Apply {
		m.blocks[b.Hash] = b
	}
	m.tip = u.Apply[len(u.Apply)-1].Hash
	return nil
}
