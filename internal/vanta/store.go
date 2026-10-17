package vanta

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/secretbox"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/share"
)

// Config is integrations.config for kind 'vanta'. It holds nothing secret.
type Config struct {
	Enabled      bool   `json:"enabled"`
	ResourceID   string `json:"resource_id"`           // the custom resource's ID in the Vanta developer console
	DocumentID   string `json:"document_id,omitempty"` // the Vanta document packs are uploaded to; empty: no uploads
	ClientIDHint string `json:"client_id_hint"`        // the client ID's last four characters, to recognise it
	ConnectedBy  string `json:"connected_by"`          // member ID: the Vanta share links are made in their name
	Resources    int    `json:"resources"`             // sent in the last successful sync
	Uploaded     int    `json:"uploaded"`              // packs uploaded in the last sync
}

// Secrets is what is sealed in integration_secrets.
type Secrets struct {
	ClientID     string          `json:"client_id"`
	ClientSecret string          `json:"client_secret"`
	Links        map[string]Link `json:"links,omitempty"` // by ai_systems.id
}

// Link is the share link the sync made for one system's verifyUrl.
// Token is empty once the vendor has revoked it: the sync then sends no
// verify link for that system rather than overriding the vendor.
type Link struct {
	ID        string    `json:"id"`
	Token     string    `json:"token,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Integration is the tenant's Vanta integration as stored.
type Integration struct {
	ID        string
	TenantID  string
	Config    Config
	LastSync  *time.Time
	LastError string

	secretID string
}

// Input is what the vendor submits in Settings. An empty ClientSecret keeps
// the stored credentials (the form never shows them again).
type Input struct {
	ClientID, ClientSecret, ResourceID, DocumentID string
}

// ErrInvalid is an input the form must reject.
var ErrInvalid = errors.New("vanta: invalid settings")

var idRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func (in *Input) clean() error {
	in.ClientID, in.ClientSecret = strings.TrimSpace(in.ClientID), strings.TrimSpace(in.ClientSecret)
	in.ResourceID, in.DocumentID = strings.TrimSpace(in.ResourceID), strings.TrimSpace(in.DocumentID)
	switch {
	case !idRE.MatchString(in.ResourceID):
		return fmt.Errorf("%w: paste the custom resource's ID from the Vanta developer console", ErrInvalid)
	case in.DocumentID != "" && !idRE.MatchString(in.DocumentID):
		return fmt.Errorf("%w: the document ID is letters, digits, dots, colons, dashes or underscores", ErrInvalid)
	case len(in.ClientID) > 200 || strings.ContainsAny(in.ClientID, " \t\r\n"):
		return fmt.Errorf("%w: the client ID is one line of at most 200 characters", ErrInvalid)
	case len(in.ClientSecret) > 500 || strings.ContainsAny(in.ClientSecret, " \t\r\n"):
		return fmt.Errorf("%w: the client secret is one line of at most 500 characters", ErrInvalid)
	}
	return nil
}

func binding(tenantID, secretID string) string {
	return "plumbline.integration_secrets:" + tenantID + ":" + secretID
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Load reads the tenant's integration (tx scoped to the tenant); nil if none.
func Load(ctx context.Context, tx pgx.Tx) (*Integration, error) {
	var in Integration
	var cfg []byte
	var ref string
	err := tx.QueryRow(ctx, `SELECT id, tenant_id, secret_ref, config, last_sync_at, coalesce(last_error, '') FROM integrations WHERE kind = 'vanta'`).
		Scan(&in.ID, &in.TenantID, &ref, &cfg, &in.LastSync, &in.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(cfg, &in.Config)
	in.secretID = strings.TrimPrefix(ref, "integration_secrets:")
	if in.secretID == ref {
		in.secretID = ""
	}
	return &in, nil
}

// ErrKey means the sealed credentials cannot be opened: INTEGRATION_KEY
// changed, or the row was tampered with. The vendor re-enters them.
var ErrKey = errors.New("vanta: the stored credentials cannot be read; enter them again")

// Secrets opens the integration's sealed credentials.
func (in *Integration) Secrets(ctx context.Context, tx pgx.Tx, box *secretbox.Box) (*Secrets, error) {
	if in.secretID == "" {
		return nil, ErrKey
	}
	var keyID string
	var sealed []byte
	if err := tx.QueryRow(ctx, `SELECT key_id, ciphertext FROM integration_secrets WHERE id = $1`, in.secretID).Scan(&keyID, &sealed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrKey
		}
		return nil, err
	}
	if keyID != box.KeyID() {
		return nil, ErrKey
	}
	plain, err := box.Open(sealed, binding(in.TenantID, in.secretID))
	if err != nil {
		return nil, ErrKey
	}
	var s Secrets
	if err := json.Unmarshal(plain, &s); err != nil {
		return nil, ErrKey
	}
	return &s, nil
}

// seal stores s as the integration's sealed secrets.
func (in *Integration) seal(ctx context.Context, tx pgx.Tx, box *secretbox.Box, s *Secrets) error {
	plain, _ := json.Marshal(s)
	sealed := box.Seal(plain, binding(in.TenantID, in.secretID))
	_, err := tx.Exec(ctx, `UPDATE integration_secrets SET key_id = $2, ciphertext = $3, updated_at = now() WHERE id = $1`, in.secretID, box.KeyID(), sealed)
	return err
}

// Save connects Vanta, or updates the settings, for the member's tenant.
func Save(ctx context.Context, tx pgx.Tx, box *secretbox.Box, tenantID, memberID string, input Input) error {
	if err := input.clean(); err != nil {
		return err
	}
	in, err := Load(ctx, tx)
	if err != nil {
		return err
	}
	var s *Secrets
	if in != nil && in.secretID != "" {
		if s, err = in.Secrets(ctx, tx, box); err != nil && !errors.Is(err, ErrKey) {
			return err
		}
	}
	switch {
	case input.ClientSecret != "":
		if input.ClientID == "" {
			return fmt.Errorf("%w: enter the client ID with the secret", ErrInvalid)
		}
		links := map[string]Link(nil)
		if s != nil && s.ClientID == input.ClientID {
			links = s.Links
		}
		s = &Secrets{ClientID: input.ClientID, ClientSecret: input.ClientSecret, Links: links}
	case s == nil:
		return fmt.Errorf("%w: enter the client ID and client secret of your Vanta application", ErrInvalid)
	case input.ClientID != "" && input.ClientID != s.ClientID:
		return fmt.Errorf("%w: enter the client secret for the new client ID", ErrInvalid)
	}
	cfg := Config{Enabled: true, ResourceID: input.ResourceID, DocumentID: input.DocumentID, ConnectedBy: memberID, ClientIDHint: hint(s.ClientID)}
	if in == nil {
		in = &Integration{ID: newUUID(), TenantID: tenantID}
	} else {
		cfg.Resources, cfg.Uploaded = in.Config.Resources, in.Config.Uploaded
	}
	if in.secretID == "" {
		in.secretID = newUUID()
		if _, err := tx.Exec(ctx, `INSERT INTO integration_secrets (id, tenant_id, key_id, ciphertext) VALUES ($1, $2, $3, '\x')`, in.secretID, tenantID, box.KeyID()); err != nil {
			return err
		}
	}
	if err := in.seal(ctx, tx, box, s); err != nil {
		return err
	}
	cj, _ := json.Marshal(cfg)
	_, err = tx.Exec(ctx, `INSERT INTO integrations (id, tenant_id, kind, secret_ref, config) VALUES ($1, $2, 'vanta', $3, $4)
		ON CONFLICT (tenant_id, kind) DO UPDATE SET secret_ref = EXCLUDED.secret_ref, config = EXCLUDED.config, last_error = NULL`,
		in.ID, tenantID, "integration_secrets:"+in.secretID, string(cj))
	return err
}

func hint(id string) string {
	if len(id) <= 4 {
		return id
	}
	return id[len(id)-4:]
}

// Disconnect stops the sync, revokes the share links it made and deletes the
// sealed credentials. What is already in Vanta stays there until the vendor
// removes the integration in Vanta.
func Disconnect(ctx context.Context, tx pgx.Tx, box *secretbox.Box) error {
	in, err := Load(ctx, tx)
	if err != nil || in == nil {
		return err
	}
	if box != nil {
		if s, err := in.Secrets(ctx, tx, box); err == nil {
			for _, l := range s.Links {
				if err := share.Revoke(ctx, tx, l.ID); err != nil && !errors.Is(err, share.ErrInvalid) {
					return err
				}
			}
		}
	}
	if in.secretID != "" {
		if _, err := tx.Exec(ctx, `DELETE FROM integration_secrets WHERE id = $1`, in.secretID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE integrations SET secret_ref = '', config = config || '{"enabled": false}'::jsonb, last_error = NULL WHERE id = $1`, in.ID)
	return err
}
