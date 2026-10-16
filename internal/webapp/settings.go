package webapp

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/jobs"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/vanta"
)

// vantaView is the Vanta panel. It never holds a credential.
type vantaView struct {
	Available   bool // the server has INTEGRATION_KEY
	Admin       bool
	Connected   bool
	Config      vanta.Config
	LastSync    *time.Time
	LastError   string
	SyncPending bool
}

func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	a.renderSettings(w, r, http.StatusOK, nil)
}

func (a *App) renderSettings(w http.ResponseWriter, r *http.Request, code int, flash *Flash) {
	type m struct{ Email, Role string }
	var org string
	var members []m
	v := vantaView{Available: a.Secrets != nil, Admin: member(r).Role == "admin"}
	err := a.inTenant(r, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT name FROM tenants`).Scan(&org); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT email, role FROM members ORDER BY created_at`)
		if err != nil {
			return err
		}
		if members, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (m, error) {
			var x m
			return x, row.Scan(&x.Email, &x.Role)
		}); err != nil {
			return err
		}
		in, err := vanta.Load(r.Context(), tx)
		if err != nil || in == nil {
			return err
		}
		v.Connected, v.Config, v.LastSync, v.LastError = in.Config.Enabled, in.Config, in.LastSync, in.LastError
		return tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = 'vanta_sync' AND tenant_id = $1 AND done_at IS NULL)`,
			member(r).TenantID).Scan(&v.SyncPending)
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.render(w, r, code, "settings", page{Title: "Settings", Section: "settings", Flash: flash,
		Data: map[string]any{"Org": org, "Members": members, "Vanta": v}})
}

// adminOnly refuses integration changes from members who are not admins.
func (a *App) adminOnly(w http.ResponseWriter, r *http.Request) bool {
	if member(r).Role == "admin" {
		return true
	}
	a.renderSettings(w, r, http.StatusForbidden, bad("Only an admin of the organisation can change the Vanta integration."))
	return false
}

// enqueueSync asks for a sync of the member's tenant unless one is waiting.
func enqueueSync(r *http.Request, tx pgx.Tx) error {
	tenant := member(r).TenantID
	var pending bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = 'vanta_sync' AND tenant_id = $1 AND done_at IS NULL)`, tenant).Scan(&pending); err != nil || pending {
		return err
	}
	_, err := jobs.Enqueue(r.Context(), tx, jobs.KindVantaSync, &tenant, map[string]string{"tenant_id": tenant}, time.Time{})
	return err
}

func (a *App) saveVanta(w http.ResponseWriter, r *http.Request) {
	if !a.adminOnly(w, r) {
		return
	}
	if a.Secrets == nil {
		a.renderSettings(w, r, http.StatusServiceUnavailable, bad("Vanta cannot be connected yet: the server has no INTEGRATION_KEY."))
		return
	}
	in := vanta.Input{ClientID: r.FormValue("client_id"), ClientSecret: r.FormValue("client_secret"),
		ResourceID: r.FormValue("resource_id"), DocumentID: r.FormValue("document_id")}
	err := a.inTenant(r, func(tx pgx.Tx) error {
		m := member(r)
		if err := vanta.Save(r.Context(), tx, a.Secrets, m.TenantID, m.ID, in); err != nil {
			return err
		}
		return enqueueSync(r, tx)
	})
	if errors.Is(err, vanta.ErrInvalid) {
		a.renderSettings(w, r, http.StatusBadRequest, bad("Not saved: "+strings.TrimPrefix(err.Error(), vanta.ErrInvalid.Error()+": ")+"."))
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Log.Info("vanta connected", "tenant_id", member(r).TenantID)
	seeOther(w, r, "/settings", "vanta")
}

func (a *App) syncVanta(w http.ResponseWriter, r *http.Request) {
	if !a.adminOnly(w, r) {
		return
	}
	err := a.inTenant(r, func(tx pgx.Tx) error {
		in, err := vanta.Load(r.Context(), tx)
		if err != nil {
			return err
		}
		if in == nil || !in.Config.Enabled {
			return errNotFound
		}
		return enqueueSync(r, tx)
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	seeOther(w, r, "/settings", "vanta-sync")
}

func (a *App) disconnectVanta(w http.ResponseWriter, r *http.Request) {
	if !a.adminOnly(w, r) {
		return
	}
	if err := a.inTenant(r, func(tx pgx.Tx) error { return vanta.Disconnect(r.Context(), tx, a.Secrets) }); err != nil {
		a.fail(w, r, err)
		return
	}
	a.Log.Info("vanta disconnected", "tenant_id", member(r).TenantID)
	seeOther(w, r, "/settings", "vanta-off")
}
