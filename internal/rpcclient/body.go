package rpcclient

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// RawTx is the common transaction shape for quai_getBlockByNumber and WS newChainBlocksV2.
type RawTx struct {
	Hash    string      `json:"hash"`
	Type    hexUint64   `json:"type"`
	EtxType *hexUint64  `json:"etxType"`
	To      *string     `json:"to"`
	Value   *hexBigInt  `json:"value"`
	Gas     *hexUint64  `json:"gas"`   // ETX gas budget; used to cap derived output count
	Input   *hexBytes   `json:"input"` // "input" field carries lockup byte for coinbase ETXs
	Inputs  []RawTxIn   `json:"inputs"`
	Outputs []RawTxOut  `json:"outputs"`
}

type RawTxIn struct {
	PreviousOutPoint RawOutpoint `json:"previousOutPoint"`
	PubKey           string      `json:"pubkey"`
}

type RawOutpoint struct {
	TxHash string    `json:"txHash"`
	Index  hexUint64 `json:"index"`
}

type RawTxOut struct {
	Address      string     `json:"address"`
	Denomination hexUint64  `json:"denomination"`
	Lock         *hexBigInt `json:"lock"`
}

// BlockBody normalises transactions from both quai_getBlockByNumber and WS newChainBlocksV2.
type BlockBody struct {
	Transactions []RawTx
	OutboundETXs []RawTx
}

// rpcFlat is the flat-RPC block JSON shape (quai_getBlockByNumber full=true).
type rpcFlat struct {
	Transactions []RawTx `json:"transactions"`
	OutboundEtxs []RawTx `json:"outboundEtxs"`
}

// wsNested is the newChainBlocksV2 payload shape: {woHeader, woBody}.
type wsNested struct {
	WoBody struct {
		Transactions []RawTx `json:"transactions"`
		OutboundEtxs []RawTx `json:"outboundEtxs"`
	} `json:"woBody"`
}

// ParseBody extracts transactions from flat-RPC or WS nested block JSON.
func ParseBody(raw json.RawMessage) (BlockBody, error) {
	var flat rpcFlat
	if err := json.Unmarshal(raw, &flat); err != nil {
		return BlockBody{}, fmt.Errorf("parse block body: %w", err)
	}
	if flat.Transactions != nil || flat.OutboundEtxs != nil {
		return BlockBody{Transactions: flat.Transactions, OutboundETXs: flat.OutboundEtxs}, nil
	}

	var ws wsNested
	if err := json.Unmarshal(raw, &ws); err != nil {
		return BlockBody{}, fmt.Errorf("parse ws block body: %w", err)
	}
	return BlockBody{
		Transactions: ws.WoBody.Transactions,
		OutboundETXs: ws.WoBody.OutboundEtxs,
	}, nil
}

// HexU64 decodes "0x..." JSON quantities (quoted or unquoted) into uint64.
// Exported so external packages can embed it in RPC response structs.
type HexU64 uint64

func (h *HexU64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	if err != nil {
		return err
	}
	*h = HexU64(v)
	return nil
}

type hexUint64 = HexU64

// hexBigInt decodes "0x..." quantities into *big.Int.
type hexBigInt struct{ V *big.Int }

func (h *hexBigInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	s = strings.TrimPrefix(s, "0x")
	v := new(big.Int)
	if _, ok := v.SetString(s, 16); !ok {
		return fmt.Errorf("invalid hex big int: %q", string(b))
	}
	h.V = v
	return nil
}

// hexBytes decodes a "0x..." hex string into []byte.
type hexBytes []byte

func (h *hexBytes) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	s = strings.TrimPrefix(s, "0x")
	if s == "" || s == "0" {
		*h = nil
		return nil
	}
	if len(s)%2 != 0 {
		s = "0" + s
	}
	out := make([]byte, len(s)/2)
	for i := range out {
		v, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if err != nil {
			return fmt.Errorf("hex decode: %w", err)
		}
		out[i] = byte(v)
	}
	*h = out
	return nil
}
