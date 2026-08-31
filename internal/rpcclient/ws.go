package rpcclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/leviquai/qi-index/internal/chain"
)

type wsNotification struct {
	Method string `json:"method"`
	Params struct {
		Result json.RawMessage `json:"result"`
	} `json:"params"`
}

type wsSpine struct {
	WoHeader struct {
		Hash       string `json:"hash"`
		ParentHash string `json:"parentHash"`
		Number     string `json:"number"`
	} `json:"woHeader"`
}

// ParseWSBlock extracts spine fields from a newChainBlocksV2 payload
// ({woHeader, woBody} shape).
func ParseWSBlock(raw json.RawMessage) (chain.Block, error) {
	var s wsSpine
	if err := json.Unmarshal(raw, &s); err != nil {
		return chain.Block{}, fmt.Errorf("parse ws block: %w", err)
	}
	if s.WoHeader.Hash == "" || s.WoHeader.ParentHash == "" || s.WoHeader.Number == "" {
		return chain.Block{}, fmt.Errorf("ws payload missing woHeader spine fields")
	}
	height, err := strconv.ParseUint(strings.TrimPrefix(s.WoHeader.Number, "0x"), 16, 64)
	if err != nil {
		return chain.Block{}, fmt.Errorf("parse ws block number %q: %w", s.WoHeader.Number, err)
	}
	return chain.Block{Height: height, Hash: s.WoHeader.Hash, ParentHash: s.WoHeader.ParentHash, Raw: raw}, nil
}

// StreamBlocks subscribes to newChainBlocksV2 and sends each head into out,
// reconnecting with backoff until ctx is done. onReconnect, if set, is called
// per reconnect attempt.
func StreamBlocks(ctx context.Context, wsURL string, out chan<- chain.Block, log *slog.Logger, onReconnect func()) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := streamOnce(ctx, wsURL, out, log)
		if ctx.Err() != nil {
			return
		}
		log.Warn("ws stream ended", "err", err, "retry_in", backoff)
		if onReconnect != nil {
			onReconnect()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func streamOnce(ctx context.Context, wsURL string, out chan<- chain.Block, log *slog.Logger) error {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	sub := `{"jsonrpc":"2.0","id":1,"method":"quai_subscribe","params":["newChainBlocksV2"]}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(sub)); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		var note wsNotification
		if err := json.Unmarshal(msg, &note); err != nil || note.Params.Result == nil {
			continue
		}
		b, err := ParseWSBlock(note.Params.Result)
		if err != nil {
			log.Warn("ws block parse", "err", err)
			continue
		}
		select {
		case out <- b:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
