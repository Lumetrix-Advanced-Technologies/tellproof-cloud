package server

import (
	"crypto/ed25519"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/batchsign"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/dsse"
)

// MaxBatchBytes caps one envelope. A batch is a few kilobytes of aggregates.
const MaxBatchBytes = 1 << 20

// ingest: POST /v1/batches with a DSSE batch envelope (D-030, D-031).
//
//	201 {digest}  stored
//	200 {digest}  this agent already sent this seq; digest is the stored one
//	              (the agent compares it to its own to tell a resend from a conflict)
//	400           not an envelope or not a valid v0.2 batch
//	401           unknown or revoked agent, or a bad signature
//	409           seq gap, or prev is not the last stored batch's digest
//	413           larger than MaxBatchBytes
func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBatchBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "the batch is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "could not read the body")
		return
	}
	env, err := dsse.Decode(body)
	if err != nil || len(env.Signatures) != 1 {
		writeError(w, http.StatusBadRequest, "the body must be a DSSE envelope with one signature")
		return
	}
	agentID := env.Signatures[0].KeyID
	ctx := r.Context()

	var tenantID, systemID string
	var pub []byte
	var revoked bool
	err = s.Store.Public(ctx, func(tx pgx.Tx) error {
		var lastSeq int64
		var lastDigest *string
		return tx.QueryRow(ctx, `SELECT tenant_id, ai_system_id, public_key, last_seq, last_digest, revoked FROM agent_lookup($1)`, agentID).
			Scan(&tenantID, &systemID, &pub, &lastSeq, &lastDigest, &revoked)
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && revoked) {
		writeError(w, http.StatusUnauthorized, "unknown or revoked agent")
		return
	}
	if err != nil {
		s.Log.Error("ingest: agent lookup failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "ingest failed")
		return
	}
	b, err := batchsign.Verify(env, ed25519.PublicKey(pub), agentID)
	if errors.Is(err, batchsign.ErrInvalid) {
		writeError(w, http.StatusBadRequest, "the payload is not a valid plumbline-batch/v0.2 or v0.3")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, "the signature does not verify with this agent's key")
		return
	}
	start, err1 := time.Parse(time.RFC3339, b.Window.Start)
	end, err2 := time.Parse(time.RFC3339, b.Window.End)
	if err1 != nil || err2 != nil || !end.After(start) {
		writeError(w, http.StatusBadRequest, "the batch window is not a valid interval")
		return
	}
	digest := batchsign.Digest(env)

	code, stored := http.StatusCreated, digest
	err = s.Store.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		// One ingest at a time per agent: the row lock orders concurrent sends.
		var lastSeq int64
		var lastDigest *string
		if err := tx.QueryRow(ctx, `SELECT last_seq, last_digest FROM agents WHERE id = $1 FOR UPDATE`, agentID).Scan(&lastSeq, &lastDigest); err != nil {
			return err
		}
		switch {
		case b.Seq <= lastSeq:
			code = http.StatusOK
			return tx.QueryRow(ctx, `SELECT digest FROM evidence_batches WHERE agent_id = $1 AND seq = $2`, agentID, b.Seq).Scan(&stored)
		case b.Seq > lastSeq+1:
			code = http.StatusConflict
			return nil
		case !samePrev(b.Prev, lastDigest):
			code = http.StatusConflict
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO evidence_batches (tenant_id, agent_id, seq, prev_digest, digest, window_start, window_end, payload, envelope)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			tenantID, agentID, b.Seq, b.Prev, digest, start, end, string(env.Payload), string(body)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE agents SET last_seq = $2, last_digest = $3 WHERE id = $1`, agentID, b.Seq, digest)
		return err
	})
	if err != nil {
		s.Log.Error("ingest failed", "agent_id", agentID, "seq", b.Seq, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "ingest failed")
		return
	}
	switch code {
	case http.StatusConflict:
		s.Log.Warn("batch refused: out of sequence", "agent_id", agentID, "seq", b.Seq)
		writeError(w, http.StatusConflict, "seq or prev does not follow the last stored batch")
	case http.StatusOK:
		writeJSON(w, http.StatusOK, map[string]string{"digest": stored})
	default:
		s.Log.Info("batch stored", "agent_id", agentID, "ai_system_id", systemID, "seq", b.Seq, "digest", digest)
		writeJSON(w, http.StatusCreated, map[string]string{"digest": digest})
	}
}

func samePrev(prev, last *string) bool {
	if prev == nil || last == nil {
		return prev == nil && last == nil
	}
	return *prev == *last
}
