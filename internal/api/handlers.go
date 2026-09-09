package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/leviquai/qi-index/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handleTip(w http.ResponseWriter, r *http.Request) {
	b, ok, err := s.reader.Tip(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no tip")
		return
	}
	var er string
	if b.ExchangeRate != nil {
		er = "0x" + b.ExchangeRate.Text(16)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"height":        b.Height,
		"hash":          b.Hash,
		"parent_hash":   b.ParentHash,
		"timestamp":     b.Timestamp,
		"exchange_rate": er,
	})
}

func (s *Server) handleGetUTXO(w http.ResponseWriter, r *http.Request) {
	txHash, err := decodeHex(r.PathValue("txhash"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid tx hash")
		return
	}
	idx, err := strconv.ParseInt(r.PathValue("index"), 10, 32)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid index")
		return
	}
	u, ok, err := s.reader.GetUTXO(r.Context(), txHash, int32(idx))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "utxo not found")
		return
	}
	writeJSON(w, http.StatusOK, marshalUTXO(u))
}

func (s *Server) handleAddressUTXOs(w http.ResponseWriter, r *http.Request) {
	addr, err := decodeHex(r.PathValue("addr"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid address")
		return
	}
	utxos, err := s.reader.GetAddressUTXOs(r.Context(), addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]any, 0, len(utxos))
	for _, u := range utxos {
		out = append(out, marshalUTXO(u))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddressBalance(w http.ResponseWriter, r *http.Request) {
	addr, err := decodeHex(r.PathValue("addr"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid address")
		return
	}
	tip, ok, err := s.reader.Tip(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var tipH uint64
	if ok {
		tipH = tip.Height
	}
	bal, err := s.reader.GetAddressBalance(r.Context(), addr, tipH)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"spendable":       bal.Spendable.String(),
		"locked":          bal.Locked.String(),
		"trimmed":         bal.Trimmed.String(),
		"spendable_count": bal.Counts.Spendable,
		"locked_count":    bal.Counts.Locked,
		"trimmed_count":   bal.Counts.Trimmed,
	})
}

func (s *Server) handleAddressHistory(w http.ResponseWriter, r *http.Request) {
	addr, err := decodeHex(r.PathValue("addr"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid address")
		return
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err2 := strconv.Atoi(l); err2 == nil {
			limit = n
		}
	}
	var cursor *store.Cursor
	if b := r.URL.Query().Get("before"); b != "" {
		cursor, err = parseCursor(b)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid cursor")
			return
		}
	}
	utxos, err := s.reader.GetAddressHistory(r.Context(), addr, cursor, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]any, 0, len(utxos))
	for _, u := range utxos {
		out = append(out, marshalUTXO(u))
	}
	var nextCursor string
	if len(utxos) == limit {
		last := utxos[len(utxos)-1]
		nextCursor = strconv.FormatUint(last.CreatedBlock, 10) + ":" + hex.EncodeToString(last.TxHash) + ":" + strconv.Itoa(int(last.TxIndex))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": nextCursor})
}

func (s *Server) handleBlockUTXOs(w http.ResponseWriter, r *http.Request) {
	height, err := strconv.ParseUint(r.PathValue("height"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid height")
		return
	}
	ev, err := s.reader.GetBlockUTXOEvents(r.Context(), height)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	marshalList := func(us []store.UTXO) []any {
		out := make([]any, 0, len(us))
		for _, u := range us {
			out = append(out, marshalUTXO(u))
		}
		return out
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": marshalList(ev.Created),
		"spent":   marshalList(ev.Spent),
		"trimmed": marshalList(ev.Trimmed),
	})
}

func (s *Server) handleOutpoints(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Addresses []string `json:"addresses"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Addresses) == 0 {
		writeErr(w, http.StatusBadRequest, "addresses must not be empty")
		return
	}
	if len(body.Addresses) > 1000 {
		writeErr(w, http.StatusBadRequest, "addresses exceeds limit of 1000")
		return
	}
	addrs := make([][]byte, 0, len(body.Addresses))
	for _, a := range body.Addresses {
		b, err := decodeHex(a)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid address: "+a)
			return
		}
		addrs = append(addrs, b)
	}
	byAddr, err := s.reader.GetOutpointsByAddresses(r.Context(), addrs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make(map[string][]map[string]string, len(byAddr))
	for addr, utxos := range byAddr {
		outpoints := make([]map[string]string, 0, len(utxos))
		for _, u := range utxos {
			op := map[string]string{
				"txHash":       store.HexHash(u.TxHash),
				"index":        fmt.Sprintf("0x%x", u.TxIndex),
				"denomination": fmt.Sprintf("0x%x", u.Denomination),
				"lock":         fmt.Sprintf("0x%x", u.LockHeight),
			}
			outpoints = append(outpoints, op)
		}
		out[addr] = outpoints
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.reader.GetStats(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tip_height":    st.TipHeight,
		"indexed_from":  st.IndexedFrom,
		"indexed_to":    st.IndexedTo,
		"total_utxos":   st.TotalUTXOs,
		"unspent_count": st.UnspentCount,
		"spent_count":   st.SpentCount,
		"trimmed_count": st.TrimmedCount,
		"total_value":   st.TotalValue.String(),
	})
}

func (s *Server) handleGetTx(w http.ResponseWriter, r *http.Request) {
	txHash, err := decodeHex(r.PathValue("txhash"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid tx hash")
		return
	}
	t, ok, err := s.reader.GetTx(r.Context(), txHash)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "tx not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": marshalList(t.Created),
		"spent":   marshalList(t.Spent),
	})
}

func marshalUTXO(u store.UTXO) map[string]any {
	m := map[string]any{
		"tx_hash":       store.HexHash(u.TxHash),
		"tx_index":      u.TxIndex,
		"address":       store.HexAddr(u.Address),
		"denomination":  u.Denomination,
		"value":         u.Value().String(),
		"lock_height":   u.LockHeight,
		"source":        u.Source,
		"created_block": u.CreatedBlock,
		"state":         u.State(),
	}
	if u.SpentBlock != nil {
		m["spent_block"] = *u.SpentBlock
	}
	if u.SpentTx != nil {
		m["spent_tx"] = store.HexHash(u.SpentTx)
	}
	if u.TrimDeadline != nil {
		m["trim_deadline"] = *u.TrimDeadline
	}
	if u.TrimmedBlock != nil {
		m["trimmed_block"] = *u.TrimmedBlock
	}
	return m
}

func decodeHex(s string) ([]byte, error) {
	return hex.DecodeString(strings.TrimPrefix(s, "0x"))
}

func parseCursor(s string) (*store.Cursor, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		return nil, strconv.ErrSyntax
	}
	block, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return nil, err
	}
	hash, err := hex.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	idx, err := strconv.ParseInt(parts[2], 10, 32)
	if err != nil {
		return nil, err
	}
	return &store.Cursor{Block: block, TxHash: hash, TxIndex: int32(idx)}, nil
}
