package webapp

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/evaluate"
)

var changeKinds = []string{"model", "prompt", "guardrail", "config", "provider"}

var refRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)

type evalRow struct {
	Suite, Model, PromptVersion, RanAt string
	PassRate                           float64
}

func (a *App) registers(w http.ResponseWriter, r *http.Request) {
	a.renderRegisters(w, r, http.StatusOK, nil)
}

func (a *App) renderRegisters(w http.ResponseWriter, r *http.Request, code int, flash *Flash) {
	var s *System
	var changes []evaluate.Change
	var incidents []evaluate.Incident
	var evals []evalRow
	err := a.inTenant(r, func(tx pgx.Tx) error {
		var err error
		if s, err = loadSystem(r, tx); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT change_date::text, kind, what FROM register_changes WHERE ai_system_id = $1 ORDER BY change_date DESC, id DESC LIMIT 100`, s.ID)
		if err != nil {
			return err
		}
		if changes, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (evaluate.Change, error) {
			var c evaluate.Change
			return c, row.Scan(&c.Date, &c.Kind, &c.What)
		}); err != nil {
			return err
		}
		rows, err = tx.Query(r.Context(), `SELECT incident_ref, opened, acknowledged, summary FROM (
			SELECT DISTINCT ON (incident_ref) * FROM register_incidents WHERE ai_system_id = $1 ORDER BY incident_ref, recorded_at DESC, id DESC) l
			ORDER BY opened DESC LIMIT 100`, s.ID)
		if err != nil {
			return err
		}
		if incidents, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (evaluate.Incident, error) {
			var i evaluate.Incident
			var opened time.Time
			var ack *time.Time
			if err := row.Scan(&i.ID, &opened, &ack, &i.Summary); err != nil {
				return i, err
			}
			i.Opened = opened.UTC().Format("2006-01-02 15:04 UTC")
			if ack != nil {
				i.Acknowledged = ack.UTC().Format("2006-01-02 15:04 UTC")
			}
			return i, nil
		}); err != nil {
			return err
		}
		rows, err = tx.Query(r.Context(), `SELECT suite, model, prompt_version, ran_at, pass_rate FROM register_evals WHERE ai_system_id = $1 ORDER BY ran_at DESC LIMIT 100`, s.ID)
		if err != nil {
			return err
		}
		evals, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (evalRow, error) {
			var e evalRow
			var at time.Time
			err := row.Scan(&e.Suite, &e.Model, &e.PromptVersion, &at, &e.PassRate)
			e.RanAt = at.UTC().Format("2006-01-02 15:04 UTC")
			return e, err
		})
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.render(w, r, code, "registers", page{Title: "Registers · " + s.Name, Section: "systems", Tab: "registers", System: s, Flash: flash,
		Data: map[string]any{"Changes": changes, "Incidents": incidents, "Evals": evals, "ChangeKinds": changeKinds}})
}

// entry is one validated register row.
type entry struct {
	kind string
	args []any
}

// parseTimeField reads an RFC 3339 time or a browser datetime-local value (UTC).
func parseTimeField(s string, required bool) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		if required {
			return nil, errors.New("a time is required")
		}
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("%q is not a date and time (use 2026-09-10T05:30:00Z)", s)
}

// validate turns one row of fields into an insertable entry.
func validate(kind string, f map[string]string) (entry, error) {
	switch kind {
	case "changes":
		d, err := time.Parse("2006-01-02", strings.TrimSpace(f["date"]))
		if err != nil {
			return entry{}, errors.New("the date must be YYYY-MM-DD")
		}
		k, what := strings.TrimSpace(f["kind"]), strings.TrimSpace(f["what"])
		ok := false
		for _, c := range changeKinds {
			ok = ok || c == k
		}
		if !ok || what == "" || len(what) > 500 {
			return entry{}, fmt.Errorf("kind must be one of %s, and what changed must be 1 to 500 characters", strings.Join(changeKinds, ", "))
		}
		return entry{kind, []any{d.Format("2006-01-02"), k, what}}, nil
	case "incidents":
		ref, summary := strings.TrimSpace(f["id"]), strings.TrimSpace(f["summary"])
		if !refRE.MatchString(ref) || summary == "" || len(summary) > 500 {
			return entry{}, errors.New("the reference is 1 to 40 letters, digits, dots, dashes or underscores, and a summary is required")
		}
		opened, err := parseTimeField(f["opened"], true)
		if err != nil {
			return entry{}, fmt.Errorf("opened: %w", err)
		}
		ack, err := parseTimeField(f["acknowledged"], false)
		if err != nil {
			return entry{}, fmt.Errorf("acknowledged: %w", err)
		}
		res, err := parseTimeField(f["resolved"], false)
		if err != nil {
			return entry{}, fmt.Errorf("resolved: %w", err)
		}
		if (ack != nil && ack.Before(*opened)) || (res != nil && res.Before(*opened)) {
			return entry{}, errors.New("acknowledged and resolved cannot be before opened")
		}
		return entry{kind, []any{ref, *opened, ack, res, summary}}, nil
	case "evals":
		suite, model, pv := strings.TrimSpace(f["suite"]), strings.TrimSpace(f["model"]), strings.TrimSpace(f["prompt_version"])
		if suite == "" || len(suite) > 128 || model == "" || len(model) > 128 || len(pv) > 128 {
			return entry{}, errors.New("suite and model are required (at most 128 characters each)")
		}
		ran, err := parseTimeField(f["ran_at"], true)
		if err != nil {
			return entry{}, fmt.Errorf("ran_at: %w", err)
		}
		rate, err := strconv.ParseFloat(strings.TrimSpace(f["pass_rate"]), 64)
		if err != nil || rate < 0 || rate > 1 {
			return entry{}, errors.New("pass_rate is a number from 0 to 1")
		}
		return entry{kind, []any{suite, model, pv, *ran, rate}}, nil
	}
	return entry{}, errNotFound
}

var registerSQL = map[string]string{
	"changes":   `INSERT INTO register_changes (tenant_id, ai_system_id, recorded_by, change_date, kind, what) VALUES ($1, $2, $3, $4, $5, $6)`,
	"incidents": `INSERT INTO register_incidents (tenant_id, ai_system_id, recorded_by, incident_ref, opened, acknowledged, resolved, summary) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
	"evals":     `INSERT INTO register_evals (tenant_id, ai_system_id, recorded_by, suite, model, prompt_version, ran_at, pass_rate) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
}

var registerColumns = map[string][]string{
	"changes":   {"date", "kind", "what"},
	"incidents": {"id", "opened", "acknowledged", "resolved", "summary"},
	"evals":     {"suite", "model", "prompt_version", "ran_at", "pass_rate"},
}

func (a *App) insert(r *http.Request, entries []entry) error {
	return a.inTenant(r, func(tx pgx.Tx) error {
		s, err := loadSystem(r, tx)
		if err != nil {
			return err
		}
		m := member(r)
		for _, e := range entries {
			if _, err := tx.Exec(r.Context(), registerSQL[e.kind], append([]any{m.TenantID, s.ID, m.ID}, e.args...)...); err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *App) addRegisterEntry(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	cols, ok := registerColumns[kind]
	if !ok {
		http.NotFound(w, r)
		return
	}
	f := map[string]string{}
	for _, c := range cols {
		f[c] = r.FormValue(c)
	}
	e, err := validate(kind, f)
	if err != nil {
		a.renderRegisters(w, r, http.StatusBadRequest, bad("Not added: "+err.Error()+"."))
		return
	}
	if err := a.insert(r, []entry{e}); err != nil {
		a.fail(w, r, err)
		return
	}
	seeOther(w, r, "/systems/"+r.PathValue("sid")+"/registers", "recorded")
}

const maxUpload = 2 << 20

// uploadRegister imports a CSV in the concierge's register format, all rows or none.
func (a *App) uploadRegister(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	cols, ok := registerColumns[kind]
	if !ok {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+1<<16)
	file, _, err := r.FormFile("file")
	if err != nil {
		a.renderRegisters(w, r, http.StatusBadRequest, bad("Choose a CSV file of at most 2 MB."))
		return
	}
	defer file.Close()
	rd := csv.NewReader(io.LimitReader(file, maxUpload))
	rd.FieldsPerRecord = -1
	header, err := rd.Read()
	if err != nil {
		a.renderRegisters(w, r, http.StatusBadRequest, bad("The file is not a CSV with a header row."))
		return
	}
	index := map[string]int{}
	for i, h := range header {
		index[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}
	for _, c := range cols {
		if _, ok := index[c]; !ok {
			a.renderRegisters(w, r, http.StatusBadRequest, bad("The header must have the columns "+strings.Join(cols, ", ")+"."))
			return
		}
	}
	var entries []entry
	for line := 2; ; line++ {
		rec, err := rd.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || len(entries) >= 10000 {
			a.renderRegisters(w, r, http.StatusBadRequest, bad(fmt.Sprintf("Nothing imported: line %d could not be read (at most 10,000 rows).", line)))
			return
		}
		f := map[string]string{}
		for _, c := range cols {
			if i := index[c]; i < len(rec) {
				f[c] = rec[i]
			}
		}
		e, err := validate(kind, f)
		if err != nil {
			a.renderRegisters(w, r, http.StatusBadRequest, bad(fmt.Sprintf("Nothing imported: line %d: %s.", line, err)))
			return
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		a.renderRegisters(w, r, http.StatusBadRequest, bad("The file has no rows."))
		return
	}
	if err := a.insert(r, entries); err != nil {
		a.fail(w, r, err)
		return
	}
	a.renderRegisters(w, r, http.StatusOK, &Flash{"ok", fmt.Sprintf("Imported %d %s.", len(entries), kind)})
}
