package vanta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/dsse"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/jobs"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/pack"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/secretbox"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/share"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store"
)

// Share links the sync makes live a year and are replaced a month before they expire.
const (
	linkLifetime = 365 * 24 * time.Hour
	linkRenew    = 30 * 24 * time.Hour
	// LinkOrg names the reviewer on the vendor's share-links page.
	LinkOrg = "Vanta (automatic sync)"
)

// Syncer is the vanta_sync job.
type Syncer struct {
	Store      *store.Store
	Box        *secretbox.Box
	Client     *Client
	AppBase    string // PUBLIC_BASE_URL: each resource's link when there is no verify link
	VerifyBase string // VERIFY_BASE_URL
	Every      time.Duration
	MaxUploads int // per tenant per run; default 10 (Vanta allows 20 requests a minute per token)
	Now        func() time.Time
	Log        *slog.Logger
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Syncer) every() time.Duration {
	if s.Every > 0 {
		return s.Every
	}
	return time.Hour
}

// NextRun is when the next hourly sync runs.
func (s *Syncer) NextRun() time.Time { return s.now().Add(s.every()) }

// Handler runs the hourly sync over every connected tenant, or, for a job
// with a tenant (Settings' "Sync now"), that tenant alone. A tenant's
// failure is stored on its integration and does not stop the others.
func (s *Syncer) Handler() jobs.Handler {
	return func(ctx context.Context, j jobs.Job) error {
		if j.TenantID != nil {
			s.SyncTenant(ctx, *j.TenantID)
			return nil
		}
		var tenants []string
		if err := s.Store.Public(ctx, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT tenant_id::text FROM vanta_tenants()`)
			if err != nil {
				return err
			}
			tenants, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		}); err != nil {
			return err
		}
		for _, t := range tenants {
			if ctx.Err() != nil {
				break
			}
			s.SyncTenant(ctx, t)
		}
		return jobs.Reschedule(ctx, s.Store, j, s.NextRun())
	}
}

// Result is what one tenant's sync did.
type Result struct {
	Skipped   bool // not connected, or another sync of the tenant was running
	Resources int
	Uploaded  int
	Err       error
}

// SyncTenant syncs one tenant and records the outcome on its integration.
func (s *Syncer) SyncTenant(ctx context.Context, tenantID string) Result {
	// One sync per tenant at a time: a second token would revoke the first.
	conn, err := s.Store.Pool.Acquire(ctx)
	if err != nil {
		return Result{Err: err}
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext('plumbline.vanta.' || $1))`, tenantID).Scan(&locked); err != nil {
		return Result{Err: err}
	}
	if !locked {
		return Result{Skipped: true}
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext('plumbline.vanta.' || $1))`, tenantID)
	}()

	res := s.sync(ctx, tenantID)
	if res.Skipped {
		return res
	}
	msg := Describe(res.Err)
	if res.Err != nil {
		s.Log.Warn("vanta sync", "tenant_id", tenantID, "resources", res.Resources, "uploaded", res.Uploaded, "error", res.Err.Error())
	} else {
		s.Log.Info("vanta sync", "tenant_id", tenantID, "resources", res.Resources, "uploaded", res.Uploaded)
	}
	if err := s.Store.WithTenant(context.WithoutCancel(ctx), tenantID, func(tx pgx.Tx) error {
		counts, _ := json.Marshal(map[string]int{"resources": res.Resources, "uploaded": res.Uploaded})
		if res.Err != nil && res.Resources == 0 {
			counts = []byte(`{}`) // keep the last good counts
		}
		_, err := tx.Exec(ctx, `UPDATE integrations SET last_sync_at = $1, last_error = nullif($2, ''), config = config || $3::jsonb WHERE kind = 'vanta'`,
			s.now(), msg, string(counts))
		return err
	}); err != nil {
		s.Log.Error("vanta sync: recording the outcome", "tenant_id", tenantID, "error", err.Error())
	}
	return res
}

// Describe is the sentence the vendor sees for a sync error ("" for none).
// It never contains a credential: errors here hold only status codes.
func Describe(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrKey):
		return "The stored Vanta credentials cannot be read. Enter the client ID and secret again."
	case Unauthorised(err):
		return "Vanta refused the client ID and secret, or the application lacks a scope. Check them in Vanta's developer console."
	case RateLimited(err):
		return "Vanta's rate limit was reached; the rest is sent in the next hourly sync."
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch {
		case ae.Op == "sync resources" && ae.Status/100 == 4:
			return fmt.Sprintf("Vanta did not accept the resources (HTTP %d). Check the resource ID and that its schema has Plumbline's properties.", ae.Status)
		case ae.Op == "upload document" && ae.Status == 404:
			return "Vanta has no document with that ID. Check the document ID, or clear it to stop uploading packs."
		}
		return fmt.Sprintf("Vanta replied HTTP %d (%s). The next hourly sync tries again.", ae.Status, ae.Op)
	}
	return "Vanta could not be reached. The next hourly sync tries again."
}

// latest is one system's latest pack.
type latest struct {
	systemID, name, environment string
	packID, digest              string
	issuedAt                    time.Time
	predicate                   pack.Predicate
}

type pendingUpload struct {
	id, packID, system, digest, period string
	envelope                           []byte
	issuedAt                           time.Time
}

func (s *Syncer) sync(ctx context.Context, tenantID string) Result {
	var res Result
	var cfg Config
	var secrets *Secrets
	var packs []latest
	var uploads []pendingUpload
	err := s.Store.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
		in, err := Load(ctx, tx)
		if err != nil {
			return err
		}
		if in == nil || !in.Config.Enabled {
			res.Skipped = true
			return nil
		}
		cfg = in.Config
		if secrets, err = in.Secrets(ctx, tx, s.Box); err != nil {
			return err
		}
		if packs, err = latestPacks(ctx, tx); err != nil {
			return err
		}
		if changed, err := s.renewLinks(ctx, tx, cfg, secrets, packs); err != nil {
			return err
		} else if changed {
			if err := in.seal(ctx, tx, s.Box, secrets); err != nil {
				return err
			}
		}
		if cfg.DocumentID != "" {
			uploads, err = pendingUploads(ctx, tx, s.maxUploads())
		}
		return err
	})
	if err != nil || res.Skipped {
		res.Err = err
		return res
	}

	scopes := ScopeWriteResource
	if cfg.DocumentID != "" {
		scopes += " " + ScopeWriteDocument
	}
	token, err := s.Client.Token(ctx, secrets.ClientID, secrets.ClientSecret, scopes)
	if err != nil {
		res.Err = err
		return res
	}
	resources := s.resources(packs, secrets.Links)
	if err := s.Client.SyncResources(ctx, token, cfg.ResourceID, resources); err != nil {
		res.Err = err
		return res
	}
	res.Resources = len(resources)
	for _, u := range uploads {
		desc := fmt.Sprintf("Plumbline evidence pack %s for %s, period %s, digest %s. Verify it with the verify page or plumbline-verify.", u.packID, u.system, u.period, u.digest)
		if err := s.Client.Upload(ctx, token, cfg.DocumentID, u.packID+".dsse.json", u.envelope, u.issuedAt, desc); err != nil {
			res.Err = err
			return res
		}
		if err := s.Store.WithTenant(ctx, tenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO vanta_uploads (pack_id, tenant_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, u.id, tenantID)
			return err
		}); err != nil {
			res.Err = err
			return res
		}
		res.Uploaded++
	}
	return res
}

func (s *Syncer) maxUploads() int {
	if s.MaxUploads > 0 {
		return s.MaxUploads
	}
	return 10
}

// latestPacks reads each system's latest pack, systems in name order.
func latestPacks(ctx context.Context, tx pgx.Tx) ([]latest, error) {
	rows, err := tx.Query(ctx, `SELECT s.id::text, s.name, s.environment, p.pack_id, p.digest, p.issued_at, p.envelope::text
		FROM ai_systems s JOIN LATERAL (SELECT * FROM packs p WHERE p.ai_system_id = s.id ORDER BY p.seq_no DESC LIMIT 1) p ON true
		ORDER BY s.name, s.environment, s.id`)
	if err != nil {
		return nil, err
	}
	var out []latest
	defer rows.Close()
	for rows.Next() {
		var l latest
		var env string
		if err := rows.Scan(&l.systemID, &l.name, &l.environment, &l.packID, &l.digest, &l.issuedAt, &env); err != nil {
			return nil, err
		}
		e, err := dsse.Decode([]byte(env))
		if err != nil {
			return nil, fmt.Errorf("vanta: pack %s: %w", l.packID, err)
		}
		st, err := pack.DecodeStatement(e.Payload)
		if err != nil {
			return nil, fmt.Errorf("vanta: pack %s: %w", l.packID, err)
		}
		l.predicate = st.Predicate
		out = append(out, l)
	}
	return out, rows.Err()
}

func pendingUploads(ctx context.Context, tx pgx.Tx, limit int) ([]pendingUpload, error) {
	rows, err := tx.Query(ctx, `SELECT p.id::text, p.pack_id, s.name, p.digest, p.issued_at, p.envelope::text
		FROM packs p JOIN ai_systems s ON s.id = p.ai_system_id
		WHERE NOT EXISTS (SELECT 1 FROM vanta_uploads u WHERE u.pack_id = p.id)
		ORDER BY p.issued_at, p.seq_no LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (pendingUpload, error) {
		var u pendingUpload
		var env string
		if err := row.Scan(&u.id, &u.packID, &u.system, &u.digest, &u.issuedAt, &env); err != nil {
			return u, err
		}
		u.envelope = []byte(env)
		if e, err := dsse.Decode(u.envelope); err == nil {
			if st, err := pack.DecodeStatement(e.Payload); err == nil {
				u.period = st.Predicate.Period.Start + " to " + st.Predicate.Period.End
			}
		}
		return u, nil
	})
}

// renewLinks makes sure each system with a pack has a live share link for
// Vanta's verifyUrl: one is made when there is none, or a month before the
// current one expires. A link the vendor revoked stays revoked.
func (s *Syncer) renewLinks(ctx context.Context, tx pgx.Tx, cfg Config, sec *Secrets, packs []latest) (bool, error) {
	if s.VerifyBase == "" || cfg.ConnectedBy == "" {
		return false, nil
	}
	if sec.Links == nil {
		sec.Links = map[string]Link{}
	}
	now := s.now()
	changed := false
	for _, p := range packs {
		l, ok := sec.Links[p.systemID]
		if ok && l.Token != "" {
			var revoked bool
			err := tx.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM share_links WHERE id = $1`, l.ID).Scan(&revoked)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return false, err
			}
			if revoked || errors.Is(err, pgx.ErrNoRows) {
				sec.Links[p.systemID] = Link{ID: l.ID, ExpiresAt: l.ExpiresAt}
				changed = true
				continue
			}
			if l.ExpiresAt.Sub(now) > linkRenew {
				continue
			}
		}
		if ok && l.Token == "" {
			continue // revoked by the vendor
		}
		nl, err := share.Create(ctx, tx, cfg.ConnectedBy, share.Target{SystemID: p.systemID}, LinkOrg, now.Add(linkLifetime), now)
		if err != nil {
			return false, err
		}
		if ok {
			if err := share.Revoke(ctx, tx, l.ID); err != nil && !errors.Is(err, share.ErrInvalid) {
				return false, err
			}
		}
		sec.Links[p.systemID] = Link{ID: nl.ID, Token: nl.Token, ExpiresAt: nl.ExpiresAt}
		changed = true
	}
	return changed, nil
}

// resources is the full state: one resource per objective per system, from
// each system's latest pack.
func (s *Syncer) resources(packs []latest, links map[string]Link) []Resource {
	out := []Resource{}
	for _, p := range packs {
		verify := ""
		if l, ok := links[p.systemID]; ok && l.Token != "" {
			verify = strings.TrimRight(s.VerifyBase, "/") + "/#t=" + l.Token
		}
		external := verify
		if external == "" {
			external = strings.TrimRight(s.AppBase, "/") + "/systems/" + p.systemID + "/packs"
		}
		system := p.name
		if p.environment != "" {
			system += " (" + p.environment + ")"
		}
		for _, o := range p.predicate.Objectives {
			out = append(out, Resource{
				DisplayName: system + " · " + o.ID + " " + o.Title,
				UniqueID:    "plumbline:" + p.systemID + ":" + o.ID,
				ExternalURL: external,
				CustomProperties: map[string]any{
					"objectiveId":   o.ID,
					"objective":     o.Title,
					"system":        system,
					"status":        o.Status,
					"passing":       o.Status == "pass",
					"packId":        p.packID,
					"packDigest":    p.digest,
					"periodStart":   p.predicate.Period.Start,
					"periodEnd":     p.predicate.Period.End,
					"lastEvaluated": p.issuedAt.UTC().Format(time.RFC3339),
					"verifyUrl":     verify,
				},
			})
		}
	}
	return out
}
