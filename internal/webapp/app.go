// Package webapp is the vendor's web app (P5-3, D-046): server-rendered
// html/template pages on the brand tokens, forms that work without
// JavaScript. Every query runs in the signed-in member's tenant scope, every
// form is Origin-checked, and one-time secrets (enrolment tokens, share links)
// are shown once in the response and never stored or redirected through a URL.
package webapp

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/auth"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/secretbox"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/store"
	"github.com/lumetrix-advanced-technologies/witnessmark/web"
)

// App serves the pages.
type App struct {
	Store *store.Store
	// Auth wraps a handler so it runs only with a session (auth.Sessions.Require);
	// requireMember also requires an organisation.
	Auth func(requireMember bool, h http.Handler) http.Handler
	// Protect refuses forms from other sites (auth.Sessions.SameOrigin).
	Protect    func(h http.Handler) http.Handler
	APIBase    string // PUBLIC_BASE_URL: where agents and reviewers reach this server
	VerifyBase string // VERIFY_BASE_URL: the static verify page
	// Secrets seals integration credentials (INTEGRATION_KEY); nil: Vanta cannot be connected.
	Secrets *secretbox.Box
	Log     *slog.Logger
	Now     func() time.Time

	pages map[string]*template.Template
}

// Timezones offered for systems (periods are whole days in one of these).
var Timezones = []string{"Australia/Sydney", "Australia/Melbourne", "Australia/Brisbane", "Australia/Adelaide", "Australia/Perth",
	"Australia/Hobart", "Pacific/Auckland", "Asia/Singapore", "Asia/Tokyo", "Asia/Kolkata", "Europe/London", "Europe/Dublin",
	"Europe/Berlin", "America/New_York", "America/Chicago", "America/Los_Angeles", "UTC"}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

var funcs = template.FuncMap{
	"fmtTime": func(t any) string {
		switch v := t.(type) {
		case time.Time:
			return v.UTC().Format("2 Jan 2006 15:04 UTC")
		case *time.Time:
			if v == nil {
				return ""
			}
			return v.UTC().Format("2 Jan 2006 15:04 UTC")
		}
		return ""
	},
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
}

// Load parses the templates.
func (a *App) Load() error {
	a.pages = map[string]*template.Template{}
	for _, p := range []string{"login", "onboarding", "systems", "system", "registers", "packs", "shares", "settings"} {
		t, err := template.New("layout").Funcs(funcs).ParseFS(web.Files, "app/templates/layout.html", "app/templates/"+p+".html")
		if err != nil {
			return fmt.Errorf("webapp: template %s: %w", p, err)
		}
		a.pages[p] = t
	}
	return nil
}

// Flash is a one-line message at the top of a page.
type Flash struct {
	Kind string // ok | bad | warn
	Text string
}

// page is what every template gets.
type page struct {
	Title, Section, Tab, Email string
	Member                     bool
	Flash                      *Flash
	System                     *System
	Data                       any
}

func (a *App) render(w http.ResponseWriter, r *http.Request, code int, name string, p page) {
	if id := auth.From(r.Context()); id != nil {
		p.Email = id.Claims.Email
		p.Member = id.Member != nil
	}
	if p.Flash == nil {
		p.Flash = flashFromQuery(r)
	}
	var buf bytes.Buffer
	if err := a.pages[name].ExecuteTemplate(&buf, "layout", p); err != nil {
		a.Log.Error("render", "page", name, "error", err.Error())
		http.Error(w, "the page could not be shown", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'none'; frame-ancestors 'none'; form-action 'self'")
	h.Set("Referrer-Policy", "same-origin")
	w.WriteHeader(code)
	_, _ = buf.WriteTo(w)
}

// Messages after a redirect are chosen by code, never echoed from the URL.
var flashes = map[string]Flash{
	"welcome":      {"ok", "Your organisation is ready. Connect an evidence agent to start collecting evidence."},
	"system":       {"ok", "System added."},
	"declarations": {"ok", "Declarations saved."},
	"revoked":      {"ok", "Agent revoked. Its batches are kept; it can send no more."},
	"recorded":     {"ok", "Register entry added."},
	"issuing":      {"warn", "Issuing the pack. It appears below in a few seconds; reload to see it."},
	"link-revoked": {"ok", "Share link revoked. It stops working at once."},
	"vanta":        {"ok", "Vanta connected. The first sync runs now; reload in a minute to see its result."},
	"vanta-sync":   {"warn", "Syncing with Vanta. Reload in a minute to see the result."},
	"vanta-off":    {"ok", "Vanta disconnected. Its share links are revoked and the credentials deleted."},
}

func flashFromQuery(r *http.Request) *Flash {
	if f, ok := flashes[r.URL.Query().Get("ok")]; ok {
		return &f
	}
	return nil
}

func bad(text string) *Flash { return &Flash{"bad", text} }

func member(r *http.Request) *auth.Member { return auth.From(r.Context()).Member }

// inTenant runs fn in the member's tenant scope.
func (a *App) inTenant(r *http.Request, fn func(tx pgx.Tx) error) error {
	return a.Store.WithTenant(r.Context(), member(r).TenantID, fn)
}

var errNotFound = errors.New("not found")

func (a *App) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNotFound) || errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	a.Log.Error("webapp", "path", r.URL.Path, "error", err.Error())
	http.Error(w, "something went wrong; try again", http.StatusInternalServerError)
}

func seeOther(w http.ResponseWriter, r *http.Request, path, ok string) {
	if ok != "" {
		path += "?ok=" + ok
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// Routes mounts the app on mux.
func (a *App) Routes(mux *http.ServeMux) {
	member := func(h http.HandlerFunc) http.Handler { return a.Auth(true, h) }
	post := func(h http.HandlerFunc) http.Handler { return a.Protect(a.Auth(true, h)) }

	static, _ := fs.Sub(web.Files, ".")
	mux.HandleFunc("GET /static/{file}", func(w http.ResponseWriter, r *http.Request) {
		paths := map[string]string{"brand.css": "brand.css", "app.css": "app/app.css"}
		p, ok := paths[r.PathValue("file")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := fs.ReadFile(static, p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /login", a.login)
	mux.Handle("GET /onboarding", a.Auth(false, http.HandlerFunc(a.onboardingForm)))
	mux.Handle("POST /onboarding", a.Protect(a.Auth(false, http.HandlerFunc(a.onboard))))
	mux.Handle("GET /{$}", member(func(w http.ResponseWriter, r *http.Request) { seeOther(w, r, "/systems", "") }))
	mux.Handle("GET /systems", member(a.systems))
	mux.Handle("POST /systems", post(a.createSystem))
	mux.Handle("GET /systems/{sid}", member(a.system))
	mux.Handle("POST /systems/{sid}/declarations", post(a.saveDeclarations))
	mux.Handle("POST /systems/{sid}/enrolment-tokens", post(a.createEnrolmentToken))
	mux.Handle("POST /systems/{sid}/agents/{aid}/revoke", post(a.revokeAgent))
	mux.Handle("GET /systems/{sid}/registers", member(a.registers))
	mux.Handle("POST /systems/{sid}/registers/{kind}", post(a.addRegisterEntry))
	mux.Handle("POST /systems/{sid}/registers/{kind}/upload", post(a.uploadRegister))
	mux.Handle("GET /systems/{sid}/packs", member(a.packs))
	mux.Handle("POST /systems/{sid}/packs", post(a.issuePack))
	mux.Handle("GET /systems/{sid}/packs/{pid}/download", member(a.downloadPack))
	mux.Handle("GET /systems/{sid}/shares", member(a.shares))
	mux.Handle("POST /systems/{sid}/shares", post(a.createShare))
	mux.Handle("POST /systems/{sid}/shares/{lid}/revoke", post(a.revokeShare))
	mux.Handle("GET /settings", member(a.settings))
	mux.Handle("POST /settings/vanta", post(a.saveVanta))
	mux.Handle("POST /settings/vanta/sync", post(a.syncVanta))
	mux.Handle("POST /settings/vanta/disconnect", post(a.disconnectVanta))
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	msgs := map[string]string{
		"email":       "Enter a valid email address.",
		"link":        "That sign-in link has expired or was already used. Ask for a new one.",
		"unavailable": "Sign-in is unavailable right now. Try again in a minute.",
	}
	data := struct {
		Sent  bool
		Error string
	}{Sent: r.URL.Query().Get("sent") == "1", Error: msgs[r.URL.Query().Get("error")]}
	a.render(w, r, http.StatusOK, "login", page{Title: "Sign in", Data: data})
}

func (a *App) onboardingForm(w http.ResponseWriter, r *http.Request) {
	if auth.From(r.Context()).Member != nil {
		seeOther(w, r, "/systems", "")
		return
	}
	a.render(w, r, http.StatusOK, "onboarding", page{Title: "Set up your organisation", Data: map[string]any{"Timezones": Timezones}})
}

func validTimezone(tz string) bool {
	for _, z := range Timezones {
		if z == tz {
			return true
		}
	}
	return false
}

func (a *App) onboard(w http.ResponseWriter, r *http.Request) {
	id := auth.From(r.Context())
	org, sys, env, tz := strings.TrimSpace(r.FormValue("org")), strings.TrimSpace(r.FormValue("system")), strings.TrimSpace(r.FormValue("environment")), r.FormValue("timezone")
	if org == "" || sys == "" || env == "" || !validTimezone(tz) {
		a.render(w, r, http.StatusBadRequest, "onboarding", page{Title: "Set up your organisation", Flash: bad("Name the organisation, the system and its environment, and pick a timezone."),
			Data: map[string]any{"Timezones": Timezones}})
		return
	}
	var systemID *string
	err := a.Store.Public(r.Context(), func(tx pgx.Tx) error {
		var tenant, member string
		return tx.QueryRow(r.Context(), `SELECT tenant_id, member_id, ai_system_id FROM onboard($1, $2, $3, $4, $5, $6)`,
			id.Claims.Subject, id.Claims.Email, org, sys, env, tz).Scan(&tenant, &member, &systemID)
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if systemID == nil {
		seeOther(w, r, "/systems", "")
		return
	}
	a.Log.Info("organisation created", "ai_system_id", *systemID)
	seeOther(w, r, "/systems/"+*systemID, "welcome")
}
