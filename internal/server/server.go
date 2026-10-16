// Package server is the control plane's HTTP API (docs/architecture.md §4.2):
// agent enrolment and batch ingest today; sessions, packs and sharing in later
// tasks. Error bodies are fixed text and never echo a request. Logs carry IDs
// and digests only: never tokens, keys or batch metrics.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/keys"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store"
)

// Server holds what the handlers need.
type Server struct {
	Store *store.Store
	Log   *slog.Logger
	// ViewSecret keys the daily hash of verifier addresses (VIEW_HASH_SECRET).
	ViewSecret []byte
	// ClientIPHeader names the proxy header with the client address (Fly-Client-IP on Fly.io); empty: the socket address.
	ClientIPHeader string
}

// Handler routes the API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/agents/enrol", s.enrol)
	mux.HandleFunc("POST /v1/batches", s.ingest)
	mux.HandleFunc("GET /.well-known/jwks.json", s.jwks)
	mux.HandleFunc("GET /v1/verify/{token}", s.verify)
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

// jwks serves the issuer's signing keys (D-042). Public; verifiers fetch it
// next to each pack, so it is cacheable for a few minutes.
func (s *Server) jwks(w http.ResponseWriter, r *http.Request) {
	var body []byte
	err := s.Store.Public(r.Context(), func(tx pgx.Tx) error {
		rows, err := keys.List(r.Context(), tx)
		if err != nil {
			return err
		}
		body, err = keys.JWKS(rows)
		return err
	})
	if err != nil {
		s.Log.Error("jwks", "error", err.Error())
		writeError(w, http.StatusServiceUnavailable, "the key set is unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Access-Control-Allow-Origin", "*") // the static verify page fetches it from another origin
	_, _ = w.Write(body)
}
