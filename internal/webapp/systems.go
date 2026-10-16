package webapp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// System is an AI system as the pages show it.
type System struct {
	ID, Name, Environment, Description, Timezone, PackPrefix string
	DeclaredModels                                           []DeclaredModel
	DeclaredRegions                                          []string
}

type DeclaredModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Role     string `json:"role,omitempty"`
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// loadSystem reads one of the tenant's systems (row-level security hides others).
func loadSystem(r *http.Request, tx pgx.Tx) (*System, error) {
	id := r.PathValue("sid")
	if !uuidRE.MatchString(id) {
		return nil, errNotFound
	}
	var s System
	var models, regions []byte
	err := tx.QueryRow(r.Context(), `SELECT id, name, environment, description, timezone, pack_prefix, declared_models, declared_regions
		FROM ai_systems WHERE id = $1`, id).Scan(&s.ID, &s.Name, &s.Environment, &s.Description, &s.Timezone, &s.PackPrefix, &models, &regions)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(models, &s.DeclaredModels)
	_ = json.Unmarshal(regions, &s.DeclaredRegions)
	return &s, nil
}

type systemRow struct {
	ID, Name, Environment string
	Agents                int
	LastBatch             *time.Time
	LatestPack            *string
}

func (a *App) systems(w http.ResponseWriter, r *http.Request) {
	a.renderSystems(w, r, http.StatusOK, nil)
}

func (a *App) renderSystems(w http.ResponseWriter, r *http.Request, code int, flash *Flash) {
	var list []systemRow
	err := a.inTenant(r, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT s.id, s.name, s.environment,
			(SELECT count(*) FROM agents a WHERE a.ai_system_id = s.id AND a.revoked_at IS NULL),
			(SELECT max(b.received_at) FROM evidence_batches b JOIN agents a ON a.id = b.agent_id WHERE a.ai_system_id = s.id),
			(SELECT p.pack_id FROM packs p WHERE p.ai_system_id = s.id ORDER BY p.seq_no DESC LIMIT 1)
			FROM ai_systems s ORDER BY s.created_at, s.name`)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (systemRow, error) {
			var s systemRow
			return s, row.Scan(&s.ID, &s.Name, &s.Environment, &s.Agents, &s.LastBatch, &s.LatestPack)
		})
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.render(w, r, code, "systems", page{Title: "AI systems", Section: "systems", Flash: flash, Data: map[string]any{"Systems": list, "Timezones": Timezones}})
}

func (a *App) createSystem(w http.ResponseWriter, r *http.Request) {
	name, env, tz := strings.TrimSpace(r.FormValue("name")), strings.TrimSpace(r.FormValue("environment")), r.FormValue("timezone")
	if name == "" || len(name) > 200 || env == "" || len(env) > 40 || !validTimezone(tz) {
		a.renderSystems(w, r, http.StatusBadRequest, bad("Name the system and its environment, and pick a timezone."))
		return
	}
	var id string
	err := a.inTenant(r, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `INSERT INTO ai_systems (tenant_id, name, environment, timezone) VALUES ($1, $2, $3, $4) RETURNING id`,
			member(r).TenantID, name, env, tz).Scan(&id)
	})
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		a.renderSystems(w, r, http.StatusBadRequest, bad("A system with that name and environment already exists."))
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	seeOther(w, r, "/systems/"+id, "system")
}

type agentRow struct {
	ID, Hostname string
	Enrolled     time.Time
	LastSeq      int64
	Revoked      bool
	LastBatch    *time.Time
}

func (a *App) system(w http.ResponseWriter, r *http.Request) {
	a.renderSystem(w, r, http.StatusOK, nil, "")
}

func modelsText(ms []DeclaredModel) string {
	var lines []string
	for _, m := range ms {
		line := m.Provider + ", " + m.Model
		if m.Role != "" {
			line += ", " + m.Role
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (a *App) renderSystem(w http.ResponseWriter, r *http.Request, code int, flash *Flash, newToken string) {
	var s *System
	var agents []agentRow
	err := a.inTenant(r, func(tx pgx.Tx) error {
		var err error
		if s, err = loadSystem(r, tx); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT a.id, a.hostname, a.enrolled_at, a.last_seq, a.revoked_at IS NOT NULL,
			(SELECT max(b.received_at) FROM evidence_batches b WHERE b.agent_id = a.id)
			FROM agents a WHERE a.ai_system_id = $1 ORDER BY a.enrolled_at`, s.ID)
		if err != nil {
			return err
		}
		agents, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (agentRow, error) {
			var x agentRow
			return x, row.Scan(&x.ID, &x.Hostname, &x.Enrolled, &x.LastSeq, &x.Revoked, &x.LastBatch)
		})
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.render(w, r, code, "system", page{Title: s.Name, Section: "systems", Tab: "setup", System: s, Flash: flash, Data: map[string]any{
		"Agents": agents, "NewToken": newToken, "APIBase": a.APIBase, "Timezones": Timezones,
		"ModelsText": modelsText(s.DeclaredModels), "RegionsText": strings.Join(s.DeclaredRegions, ", "),
	}})
}

var (
	prefixRE = regexp.MustCompile(`^[A-Z0-9]{2,12}$`)
	nameRE   = regexp.MustCompile(`^[A-Za-z0-9._:/@+-]{1,128}$`)
)

// parseModels reads "provider, model[, role]" lines.
func parseModels(text string) ([]DeclaredModel, error) {
	out := []DeclaredModel{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ",", 3)
		for j := range parts {
			parts[j] = strings.TrimSpace(parts[j])
		}
		if len(parts) < 2 || !nameRE.MatchString(parts[0]) || !nameRE.MatchString(parts[1]) {
			return nil, fmt.Errorf("line %d: write provider, model and an optional role, separated by commas", i+1)
		}
		m := DeclaredModel{Provider: parts[0], Model: parts[1]}
		if len(parts) == 3 {
			if len(parts[2]) > 100 {
				return nil, fmt.Errorf("line %d: the role is too long", i+1)
			}
			m.Role = parts[2]
		}
		out = append(out, m)
	}
	return out, nil
}

func (a *App) saveDeclarations(w http.ResponseWriter, r *http.Request) {
	models, err := parseModels(r.FormValue("models"))
	if err != nil {
		a.renderSystem(w, r, http.StatusBadRequest, bad("Declared models, "+err.Error()+"."), "")
		return
	}
	regions := []string{}
	for _, reg := range strings.Split(r.FormValue("regions"), ",") {
		if reg = strings.TrimSpace(reg); reg == "" {
			continue
		}
		if !nameRE.MatchString(reg) {
			a.renderSystem(w, r, http.StatusBadRequest, bad("Declared regions are names such as ap-southeast-2, separated by commas."), "")
			return
		}
		regions = append(regions, reg)
	}
	tz, prefix, desc := r.FormValue("timezone"), strings.TrimSpace(r.FormValue("prefix")), strings.TrimSpace(r.FormValue("description"))
	if !validTimezone(tz) || !prefixRE.MatchString(prefix) || len(desc) > 500 {
		a.renderSystem(w, r, http.StatusBadRequest, bad("Pick a timezone, and use 2 to 12 capital letters or digits for the pack ID prefix."), "")
		return
	}
	mj, _ := json.Marshal(models)
	rj, _ := json.Marshal(regions)
	err = a.inTenant(r, func(tx pgx.Tx) error {
		s, err := loadSystem(r, tx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `UPDATE ai_systems SET declared_models = $2, declared_regions = $3, timezone = $4, pack_prefix = $5, description = $6 WHERE id = $1`,
			s.ID, string(mj), string(rj), tz, prefix, desc)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	seeOther(w, r, "/systems/"+r.PathValue("sid"), "declarations")
}

// createEnrolmentToken makes a one-time token valid for 24 hours and shows it once.
func (a *App) createEnrolmentToken(w http.ResponseWriter, r *http.Request) {
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	err := a.inTenant(r, func(tx pgx.Tx) error {
		s, err := loadSystem(r, tx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO enrolment_tokens (tenant_id, ai_system_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
			member(r).TenantID, s.ID, hash[:], a.now().Add(24*time.Hour))
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.renderSystem(w, r, http.StatusOK, &Flash{"ok", "Enrolment token created. Copy it now: it is not shown again."}, token)
}

func (a *App) revokeAgent(w http.ResponseWriter, r *http.Request) {
	err := a.inTenant(r, func(tx pgx.Tx) error {
		s, err := loadSystem(r, tx)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `UPDATE agents SET revoked_at = now() WHERE id = $1 AND ai_system_id = $2 AND revoked_at IS NULL`, r.PathValue("aid"), s.ID)
		if err == nil && tag.RowsAffected() != 1 {
			return errNotFound
		}
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	seeOther(w, r, "/systems/"+r.PathValue("sid"), "revoked")
}
