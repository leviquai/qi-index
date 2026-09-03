package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/leviquai/qi-index/internal/chain"
)

// BlockDecoder writes UTXO events inside the caller's pgx.Tx, so UTXO state
// commits atomically with the spine blocks row.
type BlockDecoder interface {
	DecodeApply(ctx context.Context, tx pgx.Tx, blocks []chain.Block) error
	DecodeRollback(ctx context.Context, tx pgx.Tx, blocks []chain.Block) error
}
