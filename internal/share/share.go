// Package share manages share links (docs/architecture.md §4.6, D-044): a
// vendor gives a verifier (a bank, an insurer) a link to one pack, or to the
// latest pack of a system. The token is 16 random bytes in base64url, shown
// once; only its SHA-256 is stored. Links expire and can be revoked; every
// fetch of a pack through a link is logged in verifier_views.
package share

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// MaxLifetime is the longest a link may live.
const MaxLifetime = 366 * 24 * time.Hour

// TokenPattern is a well-formed share token (what the verify page accepts).
var TokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// Hash is what is stored for a token.
func Hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Link is a created share link; Token is shown once and never stored.
type Link struct {
	ID        string
	Token     string
	ExpiresAt time.Time
}

// Target is what a link shows: one pack, or a system's latest pack.
type Target struct {
	PackID   string // packs.id (uuid)
	SystemID string // ai_systems.id: always the latest pack
}

var ErrInvalid = errors.New("share: invalid request")

// Create makes a link in tx, which must be scoped to the tenant (store.WithTenant):
// the target must be the tenant's own, which row-level security checks.
func Create(ctx context.Context, tx pgx.Tx, memberID string, t Target, verifierOrg string, expiresAt, now time.Time) (*Link, error) {
	verifierOrg = strings.TrimSpace(verifierOrg)
	switch {
	case (t.PackID == "") == (t.SystemID == ""):
		return nil, fmt.Errorf("%w: a link shows one pack or one system's latest pack", ErrInvalid)
	case verifierOrg == "" || len(verifierOrg) > 200:
		return nil, fmt.Errorf("%w: name the organisation the link is for (at most 200 characters)", ErrInvalid)
	case !expiresAt.After(now) || expiresAt.Sub(now) > MaxLifetime:
		return nil, fmt.Errorf("%w: a link expires after now and within a year", ErrInvalid)
	}
	var tenant string
	var err error
	if t.PackID != "" {
		err = tx.QueryRow(ctx, `SELECT tenant_id FROM packs WHERE id = $1`, t.PackID).Scan(&tenant)
	} else {
		err = tx.QueryRow(ctx, `SELECT tenant_id FROM ai_systems WHERE id = $1`, t.SystemID).Scan(&tenant)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: no such pack or system in this organisation", ErrInvalid) // or another tenant's: RLS hides it
	}
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	var pack, system *string
	if t.PackID != "" {
		pack = &t.PackID
	} else {
		system = &t.SystemID
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO share_links (tenant_id, pack_id, ai_system_id, token_hash, verifier_org, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, tenant, pack, system, Hash(token), verifierOrg, expiresAt, memberID).Scan(&id); err != nil {
		return nil, err
	}
	return &Link{ID: id, Token: token, ExpiresAt: expiresAt}, nil
}

// Revoke stops a link working at once.
func Revoke(ctx context.Context, tx pgx.Tx, linkID string) error {
	tag, err := tx.Exec(ctx, `UPDATE share_links SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, linkID)
	if err == nil && tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: no active link %s", ErrInvalid, linkID)
	}
	return err
}

// Summary is a link as the vendor sees it.
type Summary struct {
	ID          string
	VerifierOrg string
	PackID      *string
	SystemID    *string
	ExpiresAt   time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
	Views       int
	LastViewed  *time.Time
	Viewers     int // distinct hashed addresses
}

// List returns the tenant's links, newest first, with their view counts.
func List(ctx context.Context, tx pgx.Tx) ([]Summary, error) {
	rows, err := tx.Query(ctx, `SELECT l.id, l.verifier_org, l.pack_id, l.ai_system_id, l.expires_at, l.revoked_at, l.created_at,
			count(v.id), max(v.viewed_at), count(DISTINCT v.ip_hash)
		FROM share_links l LEFT JOIN verifier_views v ON v.share_link_id = l.id
		GROUP BY l.id ORDER BY l.created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Summary, error) {
		var s Summary
		return s, r.Scan(&s.ID, &s.VerifierOrg, &s.PackID, &s.SystemID, &s.ExpiresAt, &s.RevokedAt, &s.CreatedAt, &s.Views, &s.LastViewed, &s.Viewers)
	})
}

// Lookup is the result of a public fetch.
type Lookup struct {
	Status             string // ok | revoked | expired | no_pack
	Envelope           []byte
	Digest             string
	PrevEnvelopeDigest *string
	SeqNo              int
	PackID             string
	VerifierOrg        string
	ExpiresAt          time.Time
}

// ErrUnknown: no link has this token.
var ErrUnknown = errors.New("share: unknown link")

// Fetch resolves a token for the public verify API and logs the view (verify_lookup).
func Fetch(ctx context.Context, tx pgx.Tx, token string, ipHash []byte, uaFamily string) (*Lookup, error) {
	var l Lookup
	var env *string
	var digest, packID *string
	var seq *int
	err := tx.QueryRow(ctx, `SELECT status, envelope::text, digest, prev_envelope_digest, seq_no, pack_id, verifier_org, expires_at
		FROM verify_lookup($1, $2, $3)`, Hash(token), ipHash, uaFamily).
		Scan(&l.Status, &env, &digest, &l.PrevEnvelopeDigest, &seq, &packID, &l.VerifierOrg, &l.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnknown
	}
	if err != nil {
		return nil, err
	}
	if env != nil {
		l.Envelope = []byte(*env)
	}
	if digest != nil {
		l.Digest = *digest
	}
	if seq != nil {
		l.SeqNo = *seq
	}
	if packID != nil {
		l.PackID = *packID
	}
	return &l, nil
}

// UAFamily reduces a User-Agent to a family name, so no raw agent string is stored.
func UAFamily(ua string) string {
	u := strings.ToLower(ua)
	switch {
	case u == "":
		return "unknown"
	case strings.Contains(u, "edg/"):
		return "edge"
	case strings.Contains(u, "firefox/"):
		return "firefox"
	case strings.Contains(u, "chrome/") || strings.Contains(u, "crios/"):
		return "chrome"
	case strings.Contains(u, "safari/"):
		return "safari"
	case strings.HasPrefix(u, "curl/"):
		return "curl"
	case strings.HasPrefix(u, "tellproof-verify"):
		return "tellproof-verify"
	}
	return "other"
}
