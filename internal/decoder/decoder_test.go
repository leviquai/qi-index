package decoder

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leviquai/qi-index/internal/chain"
	"github.com/leviquai/qi-index/internal/qidecode"
)

// --- helper: fake pgx.Tx that records executed SQL ---

type capturedExec struct {
	sql  string
	args []any
}

type fakeTx struct {
	pgx.Tx // embed interface; only Exec is called by the decoder
	execs []capturedExec
}

func (f *fakeTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, capturedExec{sql: sql, args: args})
	return pgconn.CommandTag{}, nil
}

// --- tests ---

func TestApplyBlock_QiTx(t *testing.T) {
	// Build a minimal block JSON containing one Qi tx with one input and two outputs:
	// output 0: Qi-scope address (byte 1 >= 0x80)
	// output 1: Quai-scope address (byte 1 < 0x80) → conversion output
	raw := json.RawMessage(`{
		"hash": "0xaabbccdd",
		"transactions": [{
			"hash": "0x` + hex32("aabbccdd") + `",
			"type": "0x2",
			"inputs": [{
				"previousOutPoint": {
					"txHash": "0x` + hex32("deadbeef") + `",
					"index": "0x0"
				},
				"pubkey": "0x0102"
			}],
			"outputs": [
				{
					"address": "0x00` + qiAddr() + `",
					"denomination": "0x3",
					"lock": "0x0"
				},
				{
					"address": "0x00` + quaiAddr() + `",
					"denomination": "0x5",
					"lock": null
				}
			]
		}],
		"outboundEtxs": []
	}`)

	b := chain.Block{Height: 100, Hash: "0xaabbccdd", ParentHash: "0x00", Raw: raw}
	ftx := &fakeTx{}
	d := New(nil)
	if err := d.applyBlock(context.Background(), ftx, b); err != nil {
		t.Fatalf("applyBlock: %v", err)
	}

	// Expect: 1 UPDATE (spend), 1 INSERT utxos (qi-scope output), 1 INSERT conversion_outputs, 1 UPDATE trim sweep
	if !hasSQL(ftx.execs, "SET spent_block") {
		t.Error("expected spend UPDATE")
	}
	if !hasSQL(ftx.execs, "INSERT INTO utxos") {
		t.Error("expected utxo INSERT")
	}
	if !hasSQL(ftx.execs, "INSERT INTO conversion_outputs") {
		t.Error("expected conversion_output INSERT")
	}
}

func TestApplyBlock_NoQiActivity(t *testing.T) {
	raw := json.RawMessage(`{
		"hash": "0x1111",
		"transactions": [],
		"outboundEtxs": []
	}`)
	b := chain.Block{Height: 50, Hash: "0x1111", ParentHash: "0x00", Raw: raw}
	ftx := &fakeTx{}
	d := New(nil)
	if err := d.applyBlock(context.Background(), ftx, b); err != nil {
		t.Fatalf("applyBlock: %v", err)
	}
	// Only the trim sweep UPDATE should fire.
	if len(ftx.execs) != 1 {
		t.Errorf("expected 1 exec (trim sweep), got %d", len(ftx.execs))
	}
}

func TestRollbackBlock(t *testing.T) {
	b := chain.Block{Height: 200, Hash: "0xfeed", ParentHash: "0x00", Raw: json.RawMessage(`{}`)}
	ftx := &fakeTx{}
	d := New(nil)
	if err := d.rollbackBlock(context.Background(), ftx, b); err != nil {
		t.Fatalf("rollbackBlock: %v", err)
	}
	// Expect 4 statements: un-trim, un-spend, delete created, delete conversion_outputs
	if len(ftx.execs) != 4 {
		t.Errorf("expected 4 rollback execs, got %d", len(ftx.execs))
	}
}

func TestTrimDeadlineConsensus(t *testing.T) {
	// denomination 0 must have a non-zero trim deadline
	if d := qidecode.TrimDeadline(0, 1000); d <= 1000 {
		t.Errorf("denom 0 trim deadline must be > created height, got %d", d)
	}
	// denomination 6 (1 Qi) must never trim
	if d := qidecode.TrimDeadline(6, 1000); d != 0 {
		t.Errorf("denom 6 must not trim, got %d", d)
	}
}

func TestIsQiScopeBytes(t *testing.T) {
	qi := make([]byte, 20)
	qi[1] = 0x80
	if !isQiScopeBytes(qi) {
		t.Error("expected qi scope")
	}
	quai := make([]byte, 20)
	quai[1] = 0x7f
	if isQiScopeBytes(quai) {
		t.Error("expected non-qi scope")
	}
}

func TestDecodeHashAndAddr(t *testing.T) {
	h, err := decodeHash("0x" + hex32("ff00"))
	if err != nil || len(h) != 32 {
		t.Fatalf("decodeHash: err=%v len=%d", err, len(h))
	}
	a, err := decodeAddr("0x0000000000000000000000000000000000000001")
	if err != nil || len(a) != 20 {
		t.Fatalf("decodeAddr: err=%v len=%d", err, len(a))
	}
}

// --- integration test: needs a real DB (skipped when QI_TEST_DB unset) ---

func TestDecoderIntegration(t *testing.T) {
	dbURL := testDBURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Ensure utxos table exists (migration may already have run via store.NewPostgres).
	_, _ = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS utxos (
			tx_hash BYTEA NOT NULL, tx_index SMALLINT NOT NULL,
			address BYTEA NOT NULL, zone SMALLINT NOT NULL DEFAULT 0,
			denomination SMALLINT NOT NULL, lock_height BIGINT NOT NULL DEFAULT 0,
			source SMALLINT NOT NULL, created_block BIGINT NOT NULL, created_tx BYTEA NOT NULL,
			spent_block BIGINT, spent_tx BYTEA, spender_pubkey BYTEA,
			trim_deadline BIGINT, trimmed_block BIGINT,
			PRIMARY KEY (tx_hash, tx_index)
		)`)
	_, _ = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS conversion_outputs (
			tx_hash BYTEA NOT NULL, output_index SMALLINT NOT NULL,
			to_address BYTEA NOT NULL, denomination SMALLINT NOT NULL, block_height BIGINT NOT NULL,
			PRIMARY KEY (tx_hash, output_index)
		)`)

	// Clean slate
	pool.Exec(ctx, `TRUNCATE utxos, conversion_outputs`)

	d := New(nil)

	// Block with one Qi tx: create output 0 (qi-scope), then a second block spending it.
	txHash := hex32("abcdef01")
	qi := "00" + qiAddr()
	createBlock := json.RawMessage(`{
		"hash": "0x` + hex32("b1") + `",
		"transactions": [{
			"hash": "0x` + txHash + `",
			"type": "0x2",
			"inputs": [],
			"outputs": [{"address": "0x` + qi + `", "denomination": "0x3", "lock": "0x0"}]
		}],
		"outboundEtxs": []
	}`)

	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return d.DecodeApply(ctx, tx, []chain.Block{
			{Height: 10, Hash: "0x" + hex32("b1"), ParentHash: "0x00", Raw: createBlock},
		})
	})
	if err != nil {
		t.Fatalf("apply create block: %v", err)
	}

	var count int
	pool.QueryRow(ctx, `SELECT count(*) FROM utxos WHERE created_block=10`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 utxo after create block, got %d", count)
	}

	// Spend the UTXO.
	spendBlock := json.RawMessage(`{
		"hash": "0x` + hex32("b2") + `",
		"transactions": [{
			"hash": "0x` + hex32("cccccc01") + `",
			"type": "0x2",
			"inputs": [{
				"previousOutPoint": {"txHash": "0x` + txHash + `", "index": "0x0"},
				"pubkey": "0x0304"
			}],
			"outputs": []
		}],
		"outboundEtxs": []
	}`)
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return d.DecodeApply(ctx, tx, []chain.Block{
			{Height: 11, Hash: "0x" + hex32("b2"), ParentHash: "0x" + hex32("b1"), Raw: spendBlock},
		})
	})
	if err != nil {
		t.Fatalf("apply spend block: %v", err)
	}

	var spentBlock int
	pool.QueryRow(ctx, `SELECT spent_block FROM utxos WHERE created_block=10`).Scan(&spentBlock)
	if spentBlock != 11 {
		t.Errorf("expected spent_block=11, got %d", spentBlock)
	}

	// Rollback the spend block.
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return d.DecodeRollback(ctx, tx, []chain.Block{
			{Height: 11, Hash: "0x" + hex32("b2"), ParentHash: "0x" + hex32("b1"), Raw: spendBlock},
		})
	})
	if err != nil {
		t.Fatalf("rollback spend block: %v", err)
	}

	var nullSpent *int
	pool.QueryRow(ctx, `SELECT spent_block FROM utxos WHERE created_block=10`).Scan(&nullSpent)
	if nullSpent != nil {
		t.Errorf("expected spent_block=NULL after rollback, got %v", nullSpent)
	}
}

// --- test helpers ---

func testDBURL(t *testing.T) string {
	t.Helper()
	url := testEnv("QI_TEST_DB")
	if url == "" {
		t.Skip("QI_TEST_DB not set; skipping integration test")
	}
	return url
}

func testEnv(key string) string {
	return os.Getenv(key)
}

// hex32 pads a short hex string to 64 hex chars (32 bytes).
func hex32(s string) string {
	for len(s) < 64 {
		s = "0" + s
	}
	return s
}

// qiAddr returns 38 hex chars representing bytes 1..19 of a Qi-scope address
// (second byte = 0x80). The caller prepends "00" to make a full 40-char address.
func qiAddr() string {
	return "80" + "000000000000000000000000000000000000"
}

// quaiAddr returns 38 hex chars for a Quai-scope address (second byte = 0x00).
func quaiAddr() string {
	return "00" + "000000000000000000000000000000000001"
}

func hasSQL(execs []capturedExec, substr string) bool {
	for _, e := range execs {
		if strings.Contains(e.sql, substr) {
			return true
		}
	}
	return false
}
