package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/dsse"
)

const maxEnrolBody = 16 << 10

// enrol: POST /v1/agents/enrol with Authorization: Bearer <one-time token> and
// {"public_key": PEM Ed25519 SubjectPublicKeyInfo, "hostname"}; 201
// {"agent_id"} (D-030). The token is claimed and the agent stored in one
// transaction, so a failed enrolment leaves the token usable.
func (s *Server) enrol(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		writeError(w, http.StatusUnauthorized, "an enrolment token is required")
		return
	}
	var req struct {
		PublicKey string `json:"public_key"`
		Hostname  string `json:"hostname"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEnrolBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "the body must be {public_key, hostname}")
		return
	}
	key, err := dsse.ParsePublicKeyPEM([]byte(req.PublicKey))
	pub, isEd := key.(ed25519.PublicKey)
	if err != nil || !isEd {
		writeError(w, http.StatusBadRequest, "public_key must be an Ed25519 public key in PEM")
		return
	}
	hostname := req.Hostname
	if len(hostname) > 255 {
		hostname = hostname[:255]
	}
	hash := sha256.Sum256([]byte(token))
	agentID := "agt_" + randomID(24)

	var tenantID, systemID string
	err = s.Store.Public(r.Context(), func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT tenant_id, ai_system_id FROM enrolment_claim($1)`, hash[:]).Scan(&tenantID, &systemID); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `INSERT INTO agents (id, tenant_id, ai_system_id, public_key, hostname) VALUES ($1, $2, $3, $4, $5)`,
			agentID, tenantID, systemID, []byte(pub), hostname)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "the enrolment token is unknown, used or expired")
		return
	}
	if err != nil {
		s.Log.Error("enrol failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "enrolment failed")
		return
	}
	s.Log.Info("agent enrolled", "agent_id", agentID, "tenant_id", tenantID, "ai_system_id", systemID)
	writeJSON(w, http.StatusCreated, map[string]string{"agent_id": agentID})
}

const idAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// randomID returns n characters from idAlphabet, uniformly (rejection sampling).
func randomID(n int) string {
	out := make([]byte, 0, n)
	buf := make([]byte, 1)
	for len(out) < n {
		if _, err := io.ReadFull(rand.Reader, buf); err != nil {
			panic("server: no randomness: " + err.Error())
		}
		if int(buf[0]) < 256-256%len(idAlphabet) {
			out = append(out, idAlphabet[int(buf[0])%len(idAlphabet)])
		}
	}
	return string(out)
}
