// Command m0spike is the M0 de-risking spike for qi-index.
//
// It ingests a range of blocks from a Quai zone RPC endpoint over batched
// JSON-RPC and prints all Qi UTXO activity it can extract from block bodies
// alone: Qi transaction inputs (spends) and outputs (creations), plus
// coinbase and conversion ETXs that mint Qi UTXOs.
//
// This is throwaway code whose job is to answer the M0 gate question:
// does quai_getBlockByNumber(full=true) expose complete Qi ins/outs/
// denominations in JSON? (Answer per go-quai transaction_marshalling.go:
// yes — inputs[].previousOutPoint/pubkey, outputs[].address/denomination/lock.)
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	quaiTxType     = 0x0
	externalTxType = 0x1
	qiTxType       = 0x2

	crossShardEtxType       = 0x0
	coinbaseEtxType         = 0x1
	conversionEtxType       = 0x2
	coinbaseLockupEtxType   = 0x3
	conversionLockupEtxType = 0x5
	qiUnwrapEtxType         = 0x6
)

// Denominations mirrors go-quai core/types/utxo.go:38-55 (values in qits;
// 1 Qi = 1000 qits). 15 slots, index 0 = 0.001 Qi ... index 14 = 1,000,000 Qi.
var denominations = [15]int64{1, 5, 10, 50, 100, 500, 1000, 5000, 10000, 20000, 100000, 1000000, 10000000, 100000000, 1000000000}

func denomStr(d uint64) string {
	if d >= uint64(len(denominations)) {
		return fmt.Sprintf("denom%d(?)", d)
	}
	return fmt.Sprintf("%g Qi", float64(denominations[d])/1000.0)
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type outpoint struct {
	TxHash string `json:"txHash"`
	Index  hexU64 `json:"index"`
}

type txIn struct {
	PreviousOutPoint outpoint `json:"previousOutPoint"`
	PubKey           string   `json:"pubkey"`
}

type txOut struct {
	Address      string  `json:"address"`
	Denomination hexU64  `json:"denomination"`
	Lock         *string `json:"lock"`
}

type transaction struct {
	Hash    string  `json:"hash"`
	Type    hexU64  `json:"type"`
	EtxType *hexU64 `json:"etxType"`
	To      *string `json:"to"`
	Value   *string `json:"value"`
	Input   *string `json:"input"`
	Inputs  []txIn  `json:"inputs"`
	Outputs []txOut `json:"outputs"`
}

type header struct {
	Number       []string `json:"number"`
	ExchangeRate string   `json:"exchangeRate"`
}

type block struct {
	Hash         string        `json:"hash"`
	Header       header        `json:"header"`
	Transactions []transaction `json:"transactions"`
	OutboundEtxs []transaction `json:"outboundEtxs"`
}

// hexU64 decodes "0x..." quantities.
type hexU64 uint64

func (h *hexU64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	if err != nil {
		return err
	}
	*h = hexU64(v)
	return nil
}

type client struct {
	url  string
	http *http.Client
}

func (c *client) call(reqs []rpcRequest) ([]rpcResponse, error) {
	body, err := json.Marshal(reqs)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequest("POST", c.url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			lastErr = fmt.Errorf("status %d: %v", resp.StatusCode, err)
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		var out []rpcResponse
		if err := json.Unmarshal(data, &out); err != nil {
			// single (non-batch) response
			var one rpcResponse
			if err2 := json.Unmarshal(data, &one); err2 != nil {
				return nil, fmt.Errorf("decode: %w (%s)", err, data[:min(200, len(data))])
			}
			out = []rpcResponse{one}
		}
		return out, nil
	}
	return nil, lastErr
}

func (c *client) blockNumber() (uint64, error) {
	res, err := c.call([]rpcRequest{{JSONRPC: "2.0", ID: 1, Method: "quai_blockNumber", Params: []any{}}})
	if err != nil {
		return 0, err
	}
	var s string
	if err := json.Unmarshal(res[0].Result, &s); err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
}

// isQiAddress reports whether a 20-byte 0x-hex address is in Qi ledger scope
// (second byte >= 0x80, per go-quai common address scoping).
func isQiAddress(addr string) bool {
	if len(addr) < 6 {
		return false
	}
	b, err := strconv.ParseUint(addr[4:6], 16, 8)
	return err == nil && b >= 0x80
}

type stats struct {
	blocks, qiTxs, spends, creates, coinbaseQi, conversionQi int
	denomCreated                                             map[uint64]int
}

func main() {
	rpcURL := flag.String("rpc", "https://orchard.rpc.quai.network/cyprus1", "zone RPC URL")
	start := flag.Uint64("start", 0, "start height (0 = tip-count)")
	count := flag.Uint64("count", 1000, "number of blocks to ingest")
	batchSize := flag.Uint64("batch", 100, "blocks per JSON-RPC batch")
	verbose := flag.Bool("v", false, "print every Qi event")
	flag.Parse()

	c := &client{url: *rpcURL, http: &http.Client{Timeout: 60 * time.Second}}

	tip, err := c.blockNumber()
	if err != nil {
		log.Fatalf("blockNumber: %v", err)
	}
	from := *start
	if from == 0 {
		from = tip - *count
	}
	to := from + *count
	fmt.Printf("tip=%d ingesting [%d, %d) from %s\n", tip, from, to, *rpcURL)

	st := stats{denomCreated: map[uint64]int{}}
	t0 := time.Now()
	for n := from; n < to; n += *batchSize {
		end := min(n+*batchSize, to)
		reqs := make([]rpcRequest, 0, end-n)
		for i := n; i < end; i++ {
			reqs = append(reqs, rpcRequest{JSONRPC: "2.0", ID: int(i - n), Method: "quai_getBlockByNumber", Params: []any{fmt.Sprintf("0x%x", i), true}})
		}
		res, err := c.call(reqs)
		if err != nil {
			log.Fatalf("batch [%d,%d): %v", n, end, err)
		}
		for _, r := range res {
			if r.Error != nil {
				log.Printf("rpc error id=%d: %s", r.ID, r.Error.Message)
				continue
			}
			if string(r.Result) == "null" {
				continue
			}
			var b block
			if err := json.Unmarshal(r.Result, &b); err != nil {
				log.Fatalf("block decode: %v", err)
			}
			st.blocks++
			height := n + uint64(r.ID)
			scanTxs(&st, height, b.Transactions, *verbose)
			scanTxs(&st, height, b.OutboundEtxs, *verbose)
		}
		fmt.Printf("\r[%d/%d] blocks=%d qiTxs=%d spends=%d creates=%d coinbaseQiEtxs=%d conversionQiEtxs=%d",
			end-from, to-from, st.blocks, st.qiTxs, st.spends, st.creates, st.coinbaseQi, st.conversionQi)
	}
	fmt.Printf("\n\ndone in %s\n", time.Since(t0).Round(time.Millisecond))
	fmt.Printf("blocks ingested:        %d\n", st.blocks)
	fmt.Printf("qi txs (type 0x2):      %d (%d spends, %d outputs created)\n", st.qiTxs, st.spends, st.creates)
	fmt.Printf("coinbase ETXs -> Qi:    %d\n", st.coinbaseQi)
	fmt.Printf("conversion ETXs -> Qi:  %d\n", st.conversionQi)
	if len(st.denomCreated) > 0 {
		fmt.Println("denominations created by qi txs:")
		for d := uint64(0); d < 16; d++ {
			if c := st.denomCreated[d]; c > 0 {
				fmt.Printf("  %-12s x%d\n", denomStr(d), c)
			}
		}
	}
	if st.qiTxs == 0 && st.coinbaseQi == 0 && st.conversionQi == 0 {
		fmt.Println("\nno Qi activity in this range — try a different --start")
		os.Exit(1)
	}
}

func scanTxs(st *stats, height uint64, txs []transaction, verbose bool) {
	for _, tx := range txs {
		fmt.Printf("\nblock %d tx %s type=0x%x", height, tx.Hash, uint64(tx.Type))
		switch uint64(tx.Type) {
		case qiTxType:
			st.qiTxs++
			st.spends += len(tx.Inputs)
			st.creates += len(tx.Outputs)
			for _, out := range tx.Outputs {
				st.denomCreated[uint64(out.Denomination)]++
			}
			if verbose {
				fmt.Printf("\nblock %d qi tx %s: %d in, %d out\n", height, tx.Hash, len(tx.Inputs), len(tx.Outputs))
				for _, in := range tx.Inputs {
					fmt.Printf("  spend %s:%d (pubkey %s...)\n", in.PreviousOutPoint.TxHash, in.PreviousOutPoint.Index, safePrefix(in.PubKey, 18))
				}
				for i, out := range tx.Outputs {
					lock := "spendable"
					if out.Lock != nil && *out.Lock != "0x0" {
						lock = "locked until " + *out.Lock
					}
					fmt.Printf("  create %s:%d -> %s %s (%s)\n", tx.Hash, i, out.Address, denomStr(uint64(out.Denomination)), lock)
				}
			}
		case externalTxType:
			if tx.To == nil || !isQiAddress(*tx.To) || tx.EtxType == nil {
				continue
			}
			switch uint64(*tx.EtxType) {
			case coinbaseEtxType:
				st.coinbaseQi++
				if verbose {
					fmt.Printf("\nblock %d coinbase etx %s -> %s value=%s (denoms derived via FindMinDenominations + lockup byte)\n",
						height, tx.Hash, *tx.To, weiStr(tx.Value))
				}
			case conversionEtxType:
				st.conversionQi++
				if verbose {
					fmt.Printf("\nblock %d conversion etx %s -> %s value=%s (locks for ConversionLockPeriod)\n",
						height, tx.Hash, *tx.To, weiStr(tx.Value))
				}
			}
		}
	}
}

func safePrefix(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

func weiStr(v *string) string {
	if v == nil {
		return "?"
	}
	b, ok := new(big.Int).SetString(strings.TrimPrefix(*v, "0x"), 16)
	if !ok {
		return *v
	}
	return b.String()
}
