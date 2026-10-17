package webapp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/dsse"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/issue"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/jobs"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/pack"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/share"
)

type packRow struct {
	ID, PackID, Digest, Period string
	SeqNo                      int
	Issued                     time.Time
	Pass, Attention, Fail      int
}

type jobRow struct {
	Period string
	Error  string
}

// loadPacks lists the system's packs, newest first, read from their signed predicates.
func loadPacks(r *http.Request, tx pgx.Tx, systemID string) ([]packRow, error) {
	rows, err := tx.Query(r.Context(), `SELECT id, pack_id, seq_no, digest, issued_at, envelope::text FROM packs WHERE ai_system_id = $1 ORDER BY seq_no DESC`, systemID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (packRow, error) {
		var p packRow
		var env string
		if err := row.Scan(&p.ID, &p.PackID, &p.SeqNo, &p.Digest, &p.Issued, &env); err != nil {
			return p, err
		}
		if e, err := dsse.Decode([]byte(env)); err == nil {
			if st, err := pack.DecodeStatement(e.Payload); err == nil {
				pr := st.Predicate
				p.Period = pr.Period.Start + " to " + pr.Period.End
				for _, o := range pr.Objectives {
					switch o.Status {
					case "pass":
						p.Pass++
					case "attention":
						p.Attention++
					case "fail":
						p.Fail++
					}
				}
			}
		}
		return p, nil
	})
}

func (a *App) packs(w http.ResponseWriter, r *http.Request) { a.renderPacks(w, r, http.StatusOK, nil) }

func (a *App) renderPacks(w http.ResponseWriter, r *http.Request, code int, flash *Flash) {
	var s *System
	var packs []packRow
	var pending []jobRow
	err := a.inTenant(r, func(tx pgx.Tx) error {
		var err error
		if s, err = loadSystem(r, tx); err != nil {
			return err
		}
		if packs, err = loadPacks(r, tx, s.ID); err != nil {
			return err
		}
		// Issue requests still running, and those that failed in the last day.
		rows, err := tx.Query(r.Context(), `SELECT payload::text, coalesce(last_error, ''), done_at IS NOT NULL FROM jobs
			WHERE kind = 'issue_pack' AND tenant_id = $1 AND payload->>'ai_system_id' = $2
			  AND (done_at IS NULL OR (last_error IS NOT NULL AND done_at > now() - interval '1 day'))
			ORDER BY created_at DESC LIMIT 5`, member(r).TenantID, s.ID)
		if err != nil {
			return err
		}
		pending, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (jobRow, error) {
			var payload, lastErr string
			var done bool
			if err := row.Scan(&payload, &lastErr, &done); err != nil {
				return jobRow{}, err
			}
			var p issue.JobPayload
			_ = json.Unmarshal([]byte(payload), &p)
			j := jobRow{Period: p.PeriodStart + " to " + p.PeriodEnd}
			if done {
				j.Error = friendlyIssueError(lastErr)
			}
			return j, nil
		})
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	// Default period: the previous whole calendar month in the system's timezone.
	loc, _ := time.LoadLocation(s.Timezone)
	now := a.now().In(loc)
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	a.render(w, r, code, "packs", page{Title: "Packs · " + s.Name, Section: "systems", Tab: "packs", System: s, Flash: flash, Data: map[string]any{
		"Packs": packs, "Jobs": pending,
		"DefaultStart": first.AddDate(0, -1, 0).Format("2006-01-02"), "DefaultEnd": first.AddDate(0, 0, -1).Format("2006-01-02"),
	}})
}

// friendlyIssueError turns a stored job error into a sentence for the vendor.
func friendlyIssueError(e string) string {
	switch {
	case strings.Contains(e, "no committed traces"):
		return "the agent sent no traces for that period. Check the agent is running and the period covers its batches."
	case strings.Contains(e, "may not sign"):
		return "the issuer's signing key is not available. Tellproof has been told; try again later."
	case strings.Contains(e, "invalid period"):
		return "the period is not valid."
	}
	return "the pack could not be issued. Try again, and contact Tellproof if it keeps failing."
}

func (a *App) issuePack(w http.ResponseWriter, r *http.Request) {
	start, end := r.FormValue("period_start"), r.FormValue("period_end")
	ds, err1 := time.Parse("2006-01-02", start)
	de, err2 := time.Parse("2006-01-02", end)
	if err1 != nil || err2 != nil || de.Before(ds) || de.Sub(ds) > 366*24*time.Hour {
		a.renderPacks(w, r, http.StatusBadRequest, bad("Pick a first and last day, at most a year apart."))
		return
	}
	err := a.inTenant(r, func(tx pgx.Tx) error {
		s, err := loadSystem(r, tx)
		if err != nil {
			return err
		}
		tenant := member(r).TenantID
		_, err = jobs.Enqueue(r.Context(), tx, jobs.KindIssuePack, &tenant, issue.JobPayload{AISystemID: s.ID, PeriodStart: start, PeriodEnd: end}, time.Time{})
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	seeOther(w, r, "/systems/"+r.PathValue("sid")+"/packs", "issuing")
}

func (a *App) downloadPack(w http.ResponseWriter, r *http.Request) {
	var packID, env string
	err := a.inTenant(r, func(tx pgx.Tx) error {
		s, err := loadSystem(r, tx)
		if err != nil {
			return err
		}
		if !uuidRE.MatchString(r.PathValue("pid")) {
			return errNotFound
		}
		return tx.QueryRow(r.Context(), `SELECT pack_id, envelope::text FROM packs WHERE id = $1 AND ai_system_id = $2`, r.PathValue("pid"), s.ID).Scan(&packID, &env)
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+packID+`.dsse.json"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(env))
}

type linkRow struct {
	share.Summary
	PackLabel string
	Expired   bool
}

func (a *App) shares(w http.ResponseWriter, r *http.Request) {
	a.renderShares(w, r, http.StatusOK, nil, nil)
}

type newLink struct {
	Org, URL string
	Expires  time.Time
}

func (a *App) renderShares(w http.ResponseWriter, r *http.Request, code int, flash *Flash, created *newLink) {
	var s *System
	var packs []packRow
	var links []linkRow
	err := a.inTenant(r, func(tx pgx.Tx) error {
		var err error
		if s, err = loadSystem(r, tx); err != nil {
			return err
		}
		if packs, err = loadPacks(r, tx, s.ID); err != nil {
			return err
		}
		label := map[string]string{}
		for _, p := range packs {
			label[p.ID] = p.PackID
		}
		all, err := share.List(r.Context(), tx)
		if err != nil {
			return err
		}
		for _, l := range all {
			mine := (l.SystemID != nil && *l.SystemID == s.ID) || (l.PackID != nil && label[*l.PackID] != "")
			if !mine {
				continue
			}
			row := linkRow{Summary: l, Expired: !l.ExpiresAt.After(a.now())}
			if l.PackID != nil {
				row.PackLabel = label[*l.PackID]
			}
			links = append(links, row)
		}
		return nil
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.render(w, r, code, "shares", page{Title: "Share links · " + s.Name, Section: "systems", Tab: "shares", System: s, Flash: flash,
		Data: map[string]any{"Packs": packs, "Links": links, "NewLink": created}})
}

func (a *App) createShare(w http.ResponseWriter, r *http.Request) {
	days, err := strconv.Atoi(r.FormValue("days"))
	if err != nil || days < 1 || days > 365 {
		a.renderShares(w, r, http.StatusBadRequest, bad("Choose an expiry from 1 to 365 days."), nil)
		return
	}
	var l *share.Link
	org := r.FormValue("verifier_org")
	err = a.inTenant(r, func(tx pgx.Tx) error {
		s, err := loadSystem(r, tx)
		if err != nil {
			return err
		}
		t := share.Target{SystemID: s.ID}
		if p := r.FormValue("pack"); p != "" {
			if !uuidRE.MatchString(p) {
				return share.ErrInvalid
			}
			var owner string
			if err := tx.QueryRow(r.Context(), `SELECT ai_system_id FROM packs WHERE id = $1`, p).Scan(&owner); err != nil || owner != s.ID {
				return share.ErrInvalid
			}
			t = share.Target{PackID: p}
		}
		l, err = share.Create(r.Context(), tx, member(r).ID, t, org, a.now().Add(time.Duration(days)*24*time.Hour), a.now())
		return err
	})
	if errors.Is(err, share.ErrInvalid) {
		a.renderShares(w, r, http.StatusBadRequest, bad("Name the organisation the link is for, and pick a pack of this system."), nil)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	// The token goes in the fragment: browsers never send it to any server's logs.
	url := strings.TrimRight(a.VerifyBase, "/") + "/#t=" + l.Token
	a.renderShares(w, r, http.StatusOK, &Flash{"ok", "Share link created. Copy it now: it is not shown again."},
		&newLink{Org: strings.TrimSpace(org), URL: url, Expires: l.ExpiresAt})
}

func (a *App) revokeShare(w http.ResponseWriter, r *http.Request) {
	err := a.inTenant(r, func(tx pgx.Tx) error {
		if _, err := loadSystem(r, tx); err != nil {
			return err
		}
		if !uuidRE.MatchString(r.PathValue("lid")) {
			return errNotFound
		}
		return share.Revoke(r.Context(), tx, r.PathValue("lid"))
	})
	if errors.Is(err, share.ErrInvalid) {
		err = errNotFound
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	seeOther(w, r, "/systems/"+r.PathValue("sid")+"/shares", "link-revoked")
}
