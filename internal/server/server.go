// Package server is the control plane's HTTP API (docs/architecture.md §4.2):
// agent enrolment and batch ingest today; sessions, packs and sharing in later
// tasks. Error bodies are fixed text and never echo a request. Logs carry IDs
// and digests only: never tokens, keys or batch metrics.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/lumetrix-advanced-technologies/plumbline/internal/store"
)

// Server holds what the handlers need.
type Server struct {
	Store *store.Store
	Log   *slog.Logger
}

// Handler routes the API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/agents/enrol", s.enrol)
	mux.HandleFunc("POST /v1/batches", s.ingest)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.Pool.Ping(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
