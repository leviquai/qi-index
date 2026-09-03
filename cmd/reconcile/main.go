// Command reconcile backfills a block range through the decoder, then verifies
// every unspent UTXO in the DB exists in quai_getOutpointsByAddress.
//
// Usage:
//
//	reconcile -db postgres://qi:qi@127.0.0.1:5433/qi_index \
//	          -rpc https://rpc.quai.network/cyprus1 \
//	          -blocks 2000
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leviquai/qi-index/internal/chain"
	"github.com/leviquai/qi-index/internal/decoder"
	"github.com/leviquai/qi-index/internal/rpcclient"
)

func main() {
	dbURL := flag.String("db", "postgres://qi:qi@127.0.0.1:5433/qi_index", "Postgres URL")
	rpcURL := flag.String("rpc", "https://rpc.quai.network/cyprus1", "zone RPC URL")
	nBlocks := flag.Uint64("blocks", 2000, "number of recent blocks to backfill")
	verbose := flag.Bool("v", false, "debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx := context.Background()
	if err := run(ctx, *dbURL, *rpcURL, *nBlocks, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dbURL, rpcURL string, nBlocks uint64, log *slog.Logger) error {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer pool.Close()

	client := rpcclient.New(rpcURL)

	var maxH, minH uint64
	if err := pool.QueryRow(ctx, `SELECT min(height), max(height) FROM blocks`).Scan(&minH, &maxH); err != nil {
		return fmt.Errorf("height range: %w", err)
	}
	from := maxH - nBlocks + 1
	if from < minH {
		from = minH
	}
	log.Info("backfill range", "from", from, "to", maxH, "blocks", maxH-from+1)

	dec := decoder.New(log)
	const batch = 100
	done, total := uint64(0), maxH-from+1
	for h := from; h <= maxH; h += batch {
		end := h + batch
		if end > maxH+1 {
			end = maxH + 1
		}
		blocks, err := client.BlocksByRange(ctx, h, end)
		if err != nil {
			return fmt.Errorf("fetch [%d,%d): %w", h, end, err)
		}
		for _, b := range blocks {
			if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				return dec.DecodeApply(ctx, tx, []chain.Block{b})
			}); err != nil {
				return fmt.Errorf("decode block %d: %w", b.Height, err)
			}
		}
		done += uint64(len(blocks))
		fmt.Printf("\r  backfill: %d/%d blocks", done, total)
	}
	fmt.Println()

	// Node may have UTXOs outside our window; only DB-only entries are bugs.
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT encode(address, 'hex')
		FROM utxos
		WHERE created_block BETWEEN $1 AND $2
		  AND spent_block   IS NULL
		  AND trimmed_block IS NULL
		LIMIT 1000`,
		from, maxH-50,
	)
	if err != nil {
		return fmt.Errorf("sample addresses: %w", err)
	}
	var addrs []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return err
		}
		addrs = append(addrs, "0x"+a)
	}
	rows.Close()

	if len(addrs) < 500 {
		log.Warn("fewer than 500 addresses with unspent UTXOs — range may have little Qi activity",
			"found", len(addrs), "suggestion", "increase -blocks or point at a more active range")
	}
	log.Info("addresses sampled", "count", len(addrs))

	mismatches := 0
	for i, addr := range addrs {
		nodeSet, err := nodeUnspent(ctx, client, addr)
		if err != nil {
			log.Warn("rpc getOutpointsByAddress failed", "addr", addr, "err", err)
			continue
		}
		ourSet, err := dbUnspent(ctx, pool, addr)
		if err != nil {
			return fmt.Errorf("db unspent %s: %w", addr, err)
		}
		diff := diffSets(nodeSet, ourSet)
		if len(diff.onlyDB) > 0 {
			mismatches++
			log.Error("PHANTOM UTXO (DB has it, node does not)",
				"addr", addr,
				"only_db", diff.onlyDB,
			)
		}
		if (i+1)%50 == 0 {
			fmt.Printf("\r  reconcile: %d/%d addresses checked, %d mismatches", i+1, len(addrs), mismatches)
		}
	}
	fmt.Println()

	log.Info("reconciliation complete",
		"addresses_checked", len(addrs),
		"mismatches", mismatches,
	)
	if mismatches > 0 {
		return fmt.Errorf("%d address(es) have mismatched unspent sets", mismatches)
	}
	return nil
}

type rpcOutpoint struct {
	TxHash string            `json:"txHash"`
	Index  rpcclient.HexU64 `json:"index"`
}

func nodeUnspent(ctx context.Context, client *rpcclient.Client, addr string) (map[string]struct{}, error) {
	raw, err := client.CallRaw(ctx, "quai_getOutpointsByAddress", addr)
	if err != nil {
		return nil, err
	}
	// Response is either null or an array of {txHash, index} objects.
	if string(raw) == "null" { // address has no UTXOs
		return map[string]struct{}{}, nil
	}
	var ops []rpcOutpoint
	if err := json.Unmarshal(raw, &ops); err != nil {
		return nil, fmt.Errorf("unmarshal outpoints: %w", err)
	}
	set := make(map[string]struct{}, len(ops))
	for _, op := range ops {
		set[outpointKey(op.TxHash, uint64(op.Index))] = struct{}{}
	}
	return set, nil
}

func dbUnspent(ctx context.Context, pool *pgxpool.Pool, addr string) (map[string]struct{}, error) {
	addrBytes, err := hex.DecodeString(strings.TrimPrefix(addr, "0x"))
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `
		SELECT encode(tx_hash,'hex'), tx_index
		FROM utxos
		WHERE address       = $1
		  AND spent_block   IS NULL
		  AND trimmed_block IS NULL`,
		addrBytes,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]struct{}{}
	for rows.Next() {
		var txHash string
		var idx int32
		if err := rows.Scan(&txHash, &idx); err != nil {
			return nil, err
		}
		set[outpointKey("0x"+txHash, uint64(idx))] = struct{}{}
	}
	return set, rows.Err()
}

func outpointKey(txHash string, index uint64) string {
	return fmt.Sprintf("%s:%d", strings.ToLower(txHash), index)
}

type diffResult struct {
	onlyNode []string
	onlyDB   []string
}

func diffSets(node, db map[string]struct{}) diffResult {
	var r diffResult
	for k := range node {
		if _, ok := db[k]; !ok {
			r.onlyNode = append(r.onlyNode, k)
		}
	}
	for k := range db {
		if _, ok := node[k]; !ok {
			r.onlyDB = append(r.onlyDB, k)
		}
	}
	sort.Strings(r.onlyNode)
	sort.Strings(r.onlyDB)
	return r
}
