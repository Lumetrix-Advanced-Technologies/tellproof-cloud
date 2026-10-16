package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/share"
)

// verify: GET /v1/verify/{token}, public (D-044). 200 {envelope,
// prev_envelope_digest, pack_meta}; 404 for an unknown or malformed token or
// a link with no pack yet; 410 for a revoked or expired link. The token is
// never logged, and only a daily keyed hash of the viewer's address is stored.
func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*") // the static verify page runs on another origin; no credentials
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex")
	token := r.PathValue("token")
	if !share.TokenPattern.MatchString(token) {
		writeError(w, http.StatusNotFound, "no pack at this link")
		return
	}
	var l *share.Lookup
	err := s.Store.Public(r.Context(), func(tx pgx.Tx) error {
		var err error
		l, err = share.Fetch(r.Context(), tx, token, s.viewerHash(r), share.UAFamily(r.UserAgent()))
		return err
	})
	switch {
	case errors.Is(err, share.ErrUnknown):
		writeError(w, http.StatusNotFound, "no pack at this link")
		return
	case err != nil:
		s.Log.Error("verify lookup failed", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "the pack could not be loaded")
		return
	}
	switch l.Status {
	case "revoked", "expired":
		writeError(w, http.StatusGone, "this link is no longer active")
		return
	case "no_pack":
		writeError(w, http.StatusNotFound, "no pack has been issued for this link yet")
		return
	}
	s.Log.Info("pack viewed", "pack_id", l.PackID, "digest", l.Digest)
	writeJSON(w, http.StatusOK, map[string]any{
		"envelope":             json.RawMessage(l.Envelope),
		"prev_envelope_digest": l.PrevEnvelopeDigest,
		"pack_meta": map[string]any{"pack_id": l.PackID, "seq_no": l.SeqNo, "digest": l.Digest,
			"verifier_org": l.VerifierOrg, "expires_at": l.ExpiresAt.UTC().Format(time.RFC3339)},
	})
}

// viewerHash is HMAC-SHA256(ViewSecret, UTC day | client address): repeat views
// in a day are countable, addresses are not recoverable, and the salt changes daily.
func (s *Server) viewerHash(r *http.Request) []byte {
	ip := ""
	if s.ClientIPHeader != "" {
		ip = strings.TrimSpace(strings.Split(r.Header.Get(s.ClientIPHeader), ",")[0])
	}
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	if ip == "" || len(s.ViewSecret) == 0 {
		return nil
	}
	m := hmac.New(sha256.New, s.ViewSecret)
	m.Write([]byte(time.Now().UTC().Format("2006-01-02") + "|" + ip))
	return m.Sum(nil)
}
