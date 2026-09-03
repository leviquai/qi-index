package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leviquai/qi-index/internal/chain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Postgres implements Store with the rollback, apply and tip moved in a single
// transaction, so the stored tip always matches the stored blocks.
type Postgres struct {
	pool    *pgxpool.Pool
	decoder BlockDecoder // optional; nil = no UTXO decoding
}

// SetDecoder injects a BlockDecoder so UTXO events are written atomically with
// the spine inside the same transaction as ApplyUpdate.
func (p *Postgres) SetDecoder(d BlockDecoder) { p.decoder = d }

func NewPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	p := &Postgres{pool: pool}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) migrate(ctx context.Context) error {
	if _, err := p.pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY)`); err != nil {
		return fmt.Errorf("migrations table: %w", err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var applied bool
		if err := p.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, name).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := p.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (p *Postgres) Tip(ctx context.Context) (chain.Block, bool, error) {
	var b chain.Block
	var er *string
	err := p.pool.QueryRow(ctx, `
		SELECT b.hash, b.parent_hash, b.height, b.block_time, b.exchange_rate
		FROM meta m JOIN blocks b ON b.hash = m.value
		WHERE m.key = 'tip'`).Scan(&b.Hash, &b.ParentHash, &b.Height, &b.Timestamp, &er)
	if errors.Is(err, pgx.ErrNoRows) {
		return chain.Block{}, false, nil
	}
	if err != nil {
		return chain.Block{}, false, err
	}
	if er != nil {
		b.ExchangeRate = new(big.Int)
		b.ExchangeRate.SetString(*er, 16)
	}
	return b, true, nil
}

func (p *Postgres) RecentBlocks(ctx context.Context, n int) ([]chain.Block, error) {
	rows, err := p.pool.Query(ctx, `
		WITH RECURSIVE walk AS (
			SELECT b.hash, b.parent_hash, b.height, 1 AS pos
			FROM meta m JOIN blocks b ON b.hash = m.value
			WHERE m.key = 'tip'
			UNION ALL
			SELECT b.hash, b.parent_hash, b.height, w.pos + 1
			FROM blocks b JOIN walk w ON b.hash = w.parent_hash
			WHERE w.pos < $1
		)
		SELECT hash, parent_hash, height FROM walk ORDER BY pos`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chain.Block
	for rows.Next() {
		var b chain.Block
		if err := rows.Scan(&b.Hash, &b.ParentHash, &b.Height); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (p *Postgres) ApplyUpdate(ctx context.Context, u chain.Update) error {
	if len(u.Apply) == 0 {
		return fmt.Errorf("update applies no blocks")
	}
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		var tip string
		err := tx.QueryRow(ctx, `SELECT value FROM meta WHERE key='tip' FOR UPDATE`).Scan(&tip)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		cur := tip
		for _, rb := range u.Rollback {
			if rb.Hash != cur {
				return fmt.Errorf("rollback block %s is not the current tip %s", rb.Hash, cur)
			}
			cur = rb.ParentHash
		}
		if tip != "" && u.Apply[0].ParentHash != cur {
			return fmt.Errorf("apply block %s does not connect to %s", u.Apply[0].Hash, cur)
		}
		for i := 1; i < len(u.Apply); i++ {
			if u.Apply[i].ParentHash != u.Apply[i-1].Hash {
				return fmt.Errorf("apply blocks not contiguous at %s", u.Apply[i].Hash)
			}
		}

		if p.decoder != nil && len(u.Rollback) > 0 {
			if err := p.decoder.DecodeRollback(ctx, tx, u.Rollback); err != nil {
				return fmt.Errorf("utxo rollback: %w", err)
			}
		}
		for _, rb := range u.Rollback {
			if _, err := tx.Exec(ctx, `DELETE FROM blocks WHERE hash=$1`, rb.Hash); err != nil {
				return err
			}
		}
		for _, b := range u.Apply {
			var er *string
			if b.ExchangeRate != nil {
				s := b.ExchangeRate.Text(16)
				er = &s
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO blocks (hash, parent_hash, height, block_time, exchange_rate)
				VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (hash) DO NOTHING`,
				b.Hash, b.ParentHash, b.Height, b.Timestamp, er); err != nil {
				return err
			}
		}
		if p.decoder != nil {
			if err := p.decoder.DecodeApply(ctx, tx, u.Apply); err != nil {
				return fmt.Errorf("utxo apply: %w", err)
			}
		}
		newTip := u.Apply[len(u.Apply)-1].Hash
		_, err = tx.Exec(ctx, `
			INSERT INTO meta (key, value) VALUES ('tip', $1)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, newTip)
		return err
	})
}
