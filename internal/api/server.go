// Package api serves the REST and WebSocket API for the qi-index ledger.
package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/leviquai/qi-index/internal/store"
)

//go:embed openapi.yaml
var openapiYAML []byte

// Server is the HTTP server for the REST and WebSocket API.
type Server struct {
	reader store.Reader
	hub    *Hub
	log    *slog.Logger
	http   *http.Server
}

// New creates a Server bound to addr. Call Broadcast to push block events to
// WebSocket subscribers and Start to accept connections.
func New(addr string, reader store.Reader, log *slog.Logger) *Server {
	s := &Server{
		reader: reader,
		hub:    newHub(log),
		log:    log,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tip", s.handleTip)
	mux.HandleFunc("GET /utxo/{txhash}/{index}", s.handleGetUTXO)
	mux.HandleFunc("GET /address/{addr}/utxos", s.handleAddressUTXOs)
	mux.HandleFunc("GET /address/{addr}/balance", s.handleAddressBalance)
	mux.HandleFunc("GET /address/{addr}/history", s.handleAddressHistory)
	mux.HandleFunc("GET /block/{height}/utxos", s.handleBlockUTXOs)
	mux.HandleFunc("GET /tx/{txhash}", s.handleGetTx)
	mux.HandleFunc("GET /stats", s.handleStats)
	mux.HandleFunc("POST /outpoints", s.handleOutpoints)
	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Write(openapiYAML)
	})
	mux.Handle("GET /ws", s.hub)
	s.http = &http.Server{Addr: addr, Handler: cors(mux)}
	return s
}

// Broadcast serializes events and fans them out to all WebSocket clients.
func (s *Server) Broadcast(ev store.BlockUTXOEvents) {
	payload, err := json.Marshal(map[string]any{
		"created": marshalList(ev.Created),
		"spent":   marshalList(ev.Spent),
		"trimmed": marshalList(ev.Trimmed),
	})
	if err != nil {
		s.log.Warn("ws broadcast marshal", "err", err)
		return
	}
	s.hub.Broadcast(payload)
}

// Start serves until ctx is canceled, then shuts down gracefully.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("api listening", "addr", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return s.http.Shutdown(context.Background())
	}
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func marshalList(us []store.UTXO) []any {
	out := make([]any, 0, len(us))
	for _, u := range us {
		out = append(out, marshalUTXO(u))
	}
	return out
}
