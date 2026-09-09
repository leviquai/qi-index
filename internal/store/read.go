package store

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5"

	"github.com/leviquai/qi-index/internal/chain"
	"github.com/leviquai/qi-index/internal/qidecode"
)

// UTXO is a fully-hydrated outpoint row from the utxos table.
type UTXO struct {
	TxHash       []byte
	TxIndex      int32
	Address      []byte
	Denomination uint8
	LockHeight   uint64
	Source       int16
	CreatedBlock uint64
	SpentBlock   *uint64
	SpentTx      []byte
	TrimDeadline *uint64
	TrimmedBlock *uint64
}

// State returns "unspent", "spent", or "trimmed".
func (u UTXO) State() string {
	if u.TrimmedBlock != nil {
		return "trimmed"
	}
	if u.SpentBlock != nil {
		return "spent"
	}
	return "unspent"
}

// Value returns the qit value for this UTXO's denomination.
func (u UTXO) Value() *big.Int {
	return new(big.Int).Set(qidecode.DenominationValue(u.Denomination))
}

// Cursor is a stable pagination position for GetAddressHistory.
// Encode as "<block>:<hex_txhash>:<index>" in URLs.
type Cursor struct {
	Block   uint64
	TxHash  []byte
	TxIndex int32
}

// BalanceSummary aggregates spendable and locked balances for one address.
type BalanceSummary struct {
	Spendable *big.Int
	Locked    *big.Int
	Trimmed   *big.Int
	Counts    struct{ Spendable, Locked, Trimmed int64 }
}

// BlockUTXOEvents holds all UTXO state changes at a given block height.
type BlockUTXOEvents struct {
	Created []UTXO
	Spent   []UTXO
	Trimmed []UTXO
}

// Stats holds aggregate counts and value across the indexed UTXO set.
type Stats struct {
	TipHeight    uint64
	IndexedFrom  uint64
	IndexedTo    uint64
	TotalUTXOs   int64
	UnspentCount int64
	SpentCount   int64
	TrimmedCount int64
	TotalValue   *big.Int
}

// TxUTXOs holds all UTXOs created and spent by a single transaction.
type TxUTXOs struct {
	Created []UTXO
	Spent   []UTXO
}

// Reader is the query interface consumed by the API layer.
type Reader interface {
	Tip(ctx context.Context) (chain.Block, bool, error)
	GetUTXO(ctx context.Context, txHash []byte, index int32) (UTXO, bool, error)
	GetAddressUTXOs(ctx context.Context, addr []byte) ([]UTXO, error)
	GetAddressBalance(ctx context.Context, addr []byte, tipHeight uint64) (BalanceSummary, error)
	GetAddressHistory(ctx context.Context, addr []byte, before *Cursor, limit int) ([]UTXO, error)
	GetBlockUTXOEvents(ctx context.Context, height uint64) (BlockUTXOEvents, error)
	GetStats(ctx context.Context) (Stats, error)
	GetTx(ctx context.Context, txHash []byte) (TxUTXOs, bool, error)
	GetOutpointsByAddresses(ctx context.Context, addrs [][]byte) (map[string][]UTXO, error)
}

func (p *Postgres) GetUTXO(ctx context.Context, txHash []byte, index int32) (UTXO, bool, error) {
	var u UTXO
	err := p.pool.QueryRow(ctx, `
		SELECT tx_hash, tx_index, address, denomination, lock_height, source,
		       created_block, spent_block, spent_tx, trim_deadline, trimmed_block
		FROM utxos WHERE tx_hash=$1 AND tx_index=$2`,
		txHash, index,
	).Scan(
		&u.TxHash, &u.TxIndex, &u.Address, &u.Denomination, &u.LockHeight, &u.Source,
		&u.CreatedBlock, &u.SpentBlock, &u.SpentTx, &u.TrimDeadline, &u.TrimmedBlock,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return UTXO{}, false, nil
	}
	return u, err == nil, err
}

func (p *Postgres) GetAddressUTXOs(ctx context.Context, addr []byte) ([]UTXO, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT tx_hash, tx_index, address, denomination, lock_height, source,
		       created_block, spent_block, spent_tx, trim_deadline, trimmed_block
		FROM utxos
		WHERE address=$1 AND spent_block IS NULL AND trimmed_block IS NULL
		ORDER BY created_block DESC`,
		addr,
	)
	if err != nil {
		return nil, err
	}
	return scanUTXOs(rows)
}

func (p *Postgres) GetAddressBalance(ctx context.Context, addr []byte, tipHeight uint64) (BalanceSummary, error) {
	var s BalanceSummary
	s.Spendable = new(big.Int)
	s.Locked = new(big.Int)
	s.Trimmed = new(big.Int)

	// Unspent UTXOs grouped by denomination and lock state.
	rows, err := p.pool.Query(ctx, `
		SELECT denomination, lock_height, COUNT(*)
		FROM utxos
		WHERE address=$1 AND spent_block IS NULL AND trimmed_block IS NULL
		GROUP BY denomination, lock_height`,
		addr,
	)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var denom uint8
		var lockH uint64
		var cnt int64
		if err := rows.Scan(&denom, &lockH, &cnt); err != nil {
			return s, err
		}
		val := new(big.Int).Mul(qidecode.DenominationValue(denom), big.NewInt(cnt))
		if lockH == 0 || lockH <= tipHeight {
			s.Spendable.Add(s.Spendable, val)
			s.Counts.Spendable += cnt
		} else {
			s.Locked.Add(s.Locked, val)
			s.Counts.Locked += cnt
		}
	}
	if err := rows.Err(); err != nil {
		return s, err
	}

	// Trimmed UTXOs grouped by denomination.
	trows, err := p.pool.Query(ctx, `
		SELECT denomination, COUNT(*)
		FROM utxos
		WHERE address=$1 AND trimmed_block IS NOT NULL
		GROUP BY denomination`,
		addr,
	)
	if err != nil {
		return s, err
	}
	defer trows.Close()
	for trows.Next() {
		var denom uint8
		var cnt int64
		if err := trows.Scan(&denom, &cnt); err != nil {
			return s, err
		}
		val := new(big.Int).Mul(qidecode.DenominationValue(denom), big.NewInt(cnt))
		s.Trimmed.Add(s.Trimmed, val)
		s.Counts.Trimmed += cnt
	}
	return s, trows.Err()
}

func (p *Postgres) GetAddressHistory(ctx context.Context, addr []byte, before *Cursor, limit int) ([]UTXO, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if before == nil {
		rows, err = p.pool.Query(ctx, `
			SELECT tx_hash, tx_index, address, denomination, lock_height, source,
			       created_block, spent_block, spent_tx, trim_deadline, trimmed_block
			FROM utxos WHERE address=$1
			ORDER BY created_block DESC, tx_hash DESC, tx_index DESC
			LIMIT $2`,
			addr, limit,
		)
	} else {
		rows, err = p.pool.Query(ctx, `
			SELECT tx_hash, tx_index, address, denomination, lock_height, source,
			       created_block, spent_block, spent_tx, trim_deadline, trimmed_block
			FROM utxos
			WHERE address=$1
			  AND (created_block < $2
			    OR (created_block = $2 AND tx_hash < $3)
			    OR (created_block = $2 AND tx_hash = $3 AND tx_index < $4))
			ORDER BY created_block DESC, tx_hash DESC, tx_index DESC
			LIMIT $5`,
			addr, before.Block, before.TxHash, before.TxIndex, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	return scanUTXOs(rows)
}

func (p *Postgres) GetBlockUTXOEvents(ctx context.Context, height uint64) (BlockUTXOEvents, error) {
	var ev BlockUTXOEvents
	var err error

	created, err := p.pool.Query(ctx, `
		SELECT tx_hash, tx_index, address, denomination, lock_height, source,
		       created_block, spent_block, spent_tx, trim_deadline, trimmed_block
		FROM utxos WHERE created_block=$1`, height)
	if err != nil {
		return ev, fmt.Errorf("block utxo created: %w", err)
	}
	if ev.Created, err = scanUTXOs(created); err != nil {
		return ev, err
	}

	spent, err := p.pool.Query(ctx, `
		SELECT tx_hash, tx_index, address, denomination, lock_height, source,
		       created_block, spent_block, spent_tx, trim_deadline, trimmed_block
		FROM utxos WHERE spent_block=$1`, height)
	if err != nil {
		return ev, fmt.Errorf("block utxo spent: %w", err)
	}
	if ev.Spent, err = scanUTXOs(spent); err != nil {
		return ev, err
	}

	trimmed, err := p.pool.Query(ctx, `
		SELECT tx_hash, tx_index, address, denomination, lock_height, source,
		       created_block, spent_block, spent_tx, trim_deadline, trimmed_block
		FROM utxos WHERE trimmed_block=$1`, height)
	if err != nil {
		return ev, fmt.Errorf("block utxo trimmed: %w", err)
	}
	ev.Trimmed, err = scanUTXOs(trimmed)
	return ev, err
}

func (p *Postgres) GetStats(ctx context.Context) (Stats, error) {
	var s Stats
	s.TotalValue = new(big.Int)

	err := p.pool.QueryRow(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE spent_block IS NULL AND trimmed_block IS NULL),
			COUNT(*) FILTER (WHERE spent_block IS NOT NULL),
			COUNT(*) FILTER (WHERE trimmed_block IS NOT NULL),
			COALESCE(MIN(created_block), 0),
			COALESCE(MAX(created_block), 0)
		FROM utxos`).Scan(
		&s.TotalUTXOs, &s.UnspentCount, &s.SpentCount, &s.TrimmedCount,
		&s.IndexedFrom, &s.IndexedTo,
	)
	if err != nil {
		return s, err
	}

	rows, err := p.pool.Query(ctx, `
		SELECT denomination, COUNT(*)
		FROM utxos
		WHERE spent_block IS NULL AND trimmed_block IS NULL
		GROUP BY denomination`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var denom uint8
		var cnt int64
		if err := rows.Scan(&denom, &cnt); err != nil {
			return s, err
		}
		s.TotalValue.Add(s.TotalValue, new(big.Int).Mul(qidecode.DenominationValue(denom), big.NewInt(cnt)))
	}
	if err := rows.Err(); err != nil {
		return s, err
	}

	b, ok, err := p.Tip(ctx)
	if err != nil {
		return s, err
	}
	if ok {
		s.TipHeight = b.Height
	}
	return s, nil
}

func (p *Postgres) GetTx(ctx context.Context, txHash []byte) (TxUTXOs, bool, error) {
	var t TxUTXOs
	var err error

	created, err := p.pool.Query(ctx, `
		SELECT tx_hash, tx_index, address, denomination, lock_height, source,
		       created_block, spent_block, spent_tx, trim_deadline, trimmed_block
		FROM utxos WHERE tx_hash=$1`, txHash)
	if err != nil {
		return t, false, err
	}
	if t.Created, err = scanUTXOs(created); err != nil {
		return t, false, err
	}

	spent, err := p.pool.Query(ctx, `
		SELECT tx_hash, tx_index, address, denomination, lock_height, source,
		       created_block, spent_block, spent_tx, trim_deadline, trimmed_block
		FROM utxos WHERE spent_tx=$1`, txHash)
	if err != nil {
		return t, false, err
	}
	if t.Spent, err = scanUTXOs(spent); err != nil {
		return t, false, err
	}

	if len(t.Created) == 0 && len(t.Spent) == 0 {
		return t, false, nil
	}
	return t, true, nil
}

func (p *Postgres) GetOutpointsByAddresses(ctx context.Context, addrs [][]byte) (map[string][]UTXO, error) {
	result := make(map[string][]UTXO, len(addrs))
	for _, a := range addrs {
		result[HexAddr(a)] = nil
	}

	rows, err := p.pool.Query(ctx, `
		SELECT tx_hash, tx_index, address, denomination, lock_height, source,
		       created_block, spent_block, spent_tx, trim_deadline, trimmed_block
		FROM utxos
		WHERE address = ANY($1) AND spent_block IS NULL AND trimmed_block IS NULL`,
		addrs,
	)
	if err != nil {
		return nil, err
	}
	utxos, err := scanUTXOs(rows)
	if err != nil {
		return nil, err
	}
	for _, u := range utxos {
		key := HexAddr(u.Address)
		result[key] = append(result[key], u)
	}
	return result, nil
}

func scanUTXOs(rows pgx.Rows) ([]UTXO, error) {
	defer rows.Close()
	var out []UTXO
	for rows.Next() {
		var u UTXO
		if err := rows.Scan(
			&u.TxHash, &u.TxIndex, &u.Address, &u.Denomination, &u.LockHeight, &u.Source,
			&u.CreatedBlock, &u.SpentBlock, &u.SpentTx, &u.TrimDeadline, &u.TrimmedBlock,
		); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// HexAddr hex-encodes a 20-byte address with 0x prefix.
func HexAddr(b []byte) string { return "0x" + hex.EncodeToString(b) }

// HexHash hex-encodes a 32-byte hash with 0x prefix.
func HexHash(b []byte) string { return "0x" + hex.EncodeToString(b) }
