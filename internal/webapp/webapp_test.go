package webapp_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"html"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/agent"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/auth"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/dsse"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/enrol"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/issue"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/jobs"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/keys"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/kmssign"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/pack"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/server"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store/storetest"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/webapp"
)

const userA, userB = "20000000-0000-0000-0000-00000000000a", "20000000-0000-0000-0000-00000000000b"

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type rig struct {
	srv    *httptest.Server
	st     *store.Store
	owner  *pgx.Conn
	signer *kmssign.Local
	t      *testing.T
}

// newRig serves the API and the app on one origin. A request names its user
// in X-Test-User (Supabase's side is tested in internal/auth); membership,
// tenant scope and the Origin check are the real ones.
func newRig(t *testing.T) *rig {
	t.Helper()
	db := storetest.MigratedDB(t)
	st := storetest.App(t, db)
	owner, err := pgx.Connect(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close(context.Background()) })
	rg := &rig{st: st, owner: owner, t: t}
	sessions := &auth.Sessions{Store: st}
	authn := func(requireMember bool, h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid := r.Header.Get("X-Test-User")
			if uid == "" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			m, err := sessions.MemberOf(r.Context(), uid)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			if m == nil && requireMember {
				http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
				return
			}
			id := &auth.Identity{Claims: &auth.Claims{Subject: uid, Email: uid[len(uid)-1:] + "@vendor.example"}, Member: m}
			h.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
		})
	}
	mux := http.NewServeMux()
	rg.srv = httptest.NewServer(mux)
	t.Cleanup(rg.srv.Close)
	sessions.BaseURL = rg.srv.URL
	app := &webapp.App{Store: st, Auth: authn, Protect: sessions.SameOrigin, APIBase: rg.srv.URL, VerifyBase: "https://verify.example", Log: quiet()}
	if err := app.Load(); err != nil {
		t.Fatal(err)
	}
	api := &server.Server{Store: st, Log: quiet(), ViewSecret: []byte("a-view-secret-for-tests-0123"), App: app}
	mux.Handle("/", api.Handler())

	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	rg.signer, _ = kmssign.NewLocal(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err := pgx.BeginFunc(context.Background(), owner, func(tx pgx.Tx) error {
		return keys.Add(context.Background(), tx, rg.signer, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	}); err != nil {
		t.Fatal(err)
	}
	return rg
}

type resp struct {
	code     int
	body     string
	location string
	header   http.Header
}

func (rg *rig) do(user, method, path string, form url.Values, origin bool) resp {
	rg.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, rg.srv.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	if origin {
		req.Header.Set("Origin", rg.srv.URL)
	}
	return rg.send(req)
}

func (rg *rig) send(req *http.Request) resp {
	rg.t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Do(req)
	if err != nil {
		rg.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{code: r.StatusCode, body: string(b), location: r.Header.Get("Location"), header: r.Header}
}

func (rg *rig) get(user, path string) resp { return rg.do(user, http.MethodGet, path, nil, false) }

func (rg *rig) post(user, path string, form url.Values) resp {
	return rg.do(user, http.MethodPost, path, form, true)
}

func (rg *rig) upload(user, path, csvText string) resp {
	rg.t.Helper()
	var b strings.Builder
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("file", "register.csv")
	_, _ = io.WriteString(fw, csvText)
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, rg.srv.URL+path, strings.NewReader(b.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Test-User", user)
	req.Header.Set("Origin", rg.srv.URL)
	return rg.send(req)
}

func secret(t *testing.T, body, label string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<b>` + label + `</b>.*?<p class="mono">([^<]+)</p>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no %s in the page:\n%s", label, body)
	}
	return html.UnescapeString(m[1])
}

// Acceptance (P5-3): a new partner goes from sign-up to a first issued pack,
// and a reviewer verifies it, through the app's own pages.
func TestNewPartnerToFirstPackAndShare(t *testing.T) {
	rg := newRig(t)
	ctx := context.Background()

	// 1. Signed in, no organisation yet: onboarding.
	if r := rg.get(userA, "/systems"); r.code != 303 || r.location != "/onboarding" {
		t.Fatalf("before onboarding: %d %s", r.code, r.location)
	}
	if r := rg.get(userA, "/onboarding"); r.code != 200 || !strings.Contains(r.body, "Set up your organisation") {
		t.Fatalf("onboarding form: %d", r.code)
	}
	r := rg.post(userA, "/onboarding", url.Values{"org": {"Wren Health"}, "system": {"Referral Summariser"}, "environment": {"production"}, "timezone": {"UTC"}})
	if r.code != 303 || !strings.HasSuffix(r.location, "?ok=welcome") {
		t.Fatalf("onboard: %d %s", r.code, r.location)
	}
	sys := strings.TrimSuffix(r.location, "?ok=welcome")
	if again := rg.post(userA, "/onboarding", url.Values{"org": {"Second"}, "system": {"x"}, "environment": {"p"}, "timezone": {"UTC"}}); again.location != "/systems" {
		t.Errorf("onboarding twice: %d %s", again.code, again.location)
	}
	page := rg.get(userA, sys+"?ok=welcome")
	if page.code != 200 || !strings.Contains(page.body, "Referral Summariser") || !strings.Contains(page.body, "Your organisation is ready") {
		t.Fatalf("system page: %d", page.code)
	}

	// 2. Declarations, as the sample's traffic uses them.
	r = rg.post(userA, sys+"/declarations", url.Values{
		"models":  {"aws.bedrock, anthropic.claude-sonnet-4-5, triage\nopenai, gpt-4.1-mini, routing"},
		"regions": {"ap-southeast-2, us-east-1"}, "timezone": {"UTC"}, "prefix": {"WRN"}, "description": {"Summarises referrals"}})
	if r.code != 303 {
		t.Fatalf("declarations: %d %s", r.code, r.body)
	}

	// 3. Enrolment token, shown once; the real agent enrols with it and delivers the sample.
	r = rg.post(userA, sys+"/enrolment-tokens", nil)
	token := secret(t, r.body, "Enrolment token")
	if !strings.Contains(r.body, "PLUMBLINE_ENROL_TOKEN="+token) || !strings.Contains(r.body, "plumbline.init()") {
		t.Errorf("the install snippet is missing")
	}
	dir := filepath.Join(t.TempDir(), "agent")
	if _, err := enrol.Enrol(ctx, enrol.Options{Server: rg.srv.URL, Token: token, DataDir: dir, HTTP: rg.srv.Client()}); err != nil {
		t.Fatalf("enrol with the page's token: %v", err)
	}
	cfg, _ := agent.LoadConfig("", func(string) string { return "" })
	cfg.Server, cfg.DataDir = rg.srv.URL, dir
	ag, err := agent.New(cfg, agent.Options{Log: quiet(), HTTP: rg.srv.Client(), Getenv: func(k string) string {
		return map[string]string{"PLUMBLINE_SALT_SECRET": "a-salt-secret-0123456"}[k]
	}})
	if err != nil {
		t.Fatal(err)
	}
	sample, _ := os.Open(filepath.Join("..", "..", "concierge", "testdata", "otlp-sample.jsonl"))
	defer sample.Close()
	if err := ag.Replay(sample); err != nil {
		t.Fatal(err)
	}
	_ = ag.Shutdown(ctx)
	if err := ag.Deliver(ctx); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if p := rg.get(userA, sys); !strings.Contains(p.body, ag.AgentID()) || strings.Contains(p.body, "none yet</span></td>\n      <td><form") {
		t.Errorf("the agent is not listed with its batches")
	}
	if p := rg.post(userA, sys+"/enrolment-tokens", nil); secret(t, p.body, "Enrolment token") == token {
		t.Error("a second token repeated the first")
	}

	// 4. Registers: a form entry and a CSV upload.
	if r := rg.post(userA, sys+"/registers/changes", url.Values{"date": {"2026-09-01"}, "kind": {"model"}, "what": {"Triage model moved to Sonnet 4.6"}}); r.code != 303 {
		t.Errorf("add change: %d %s", r.code, r.body)
	}
	if r := rg.upload(userA, sys+"/registers/incidents/upload", "id,opened,acknowledged,resolved,summary\nINC-1,2026-09-01T00:30:00Z,2026-09-01T00:40:00Z,,Guardrail spike\n"); r.code != 200 || !strings.Contains(r.body, "Imported 1 incidents") {
		t.Errorf("upload incidents: %d", r.code)
	}

	// 5. Issue a pack; the worker issues it; the packs page lists it.
	if r := rg.post(userA, sys+"/packs", url.Values{"period_start": {"2026-09-01"}, "period_end": {"2026-09-01"}}); r.code != 303 || !strings.HasSuffix(r.location, "ok=issuing") {
		t.Fatalf("issue: %d %s", r.code, r.body)
	}
	if p := rg.get(userA, sys+"/packs"); !strings.Contains(p.body, "Issuing the pack for 2026-09-01 to 2026-09-01") {
		t.Errorf("the pending issue is not shown")
	}
	w := &jobs.Worker{Store: rg.st, Handlers: map[string]jobs.Handler{jobs.KindIssuePack: (&issue.Issuer{Store: rg.st, Signer: rg.signer}).Handler()}}
	if found, err := w.RunOnce(ctx); !found || err != nil {
		t.Fatalf("worker: %v %v", found, err)
	}
	packsPage := rg.get(userA, sys+"/packs")
	pid := regexp.MustCompile(`/packs/([0-9a-f-]{36})/download`).FindStringSubmatch(packsPage.body)
	if pid == nil || !strings.Contains(packsPage.body, "WRN-") || !strings.Contains(packsPage.body, "✓</span> Pass") {
		t.Fatalf("packs page:\n%s", packsPage.body)
	}
	dl := rg.get(userA, sys+"/packs/"+pid[1]+"/download")
	env, err := dsse.Decode([]byte(dl.body))
	if dl.code != 200 || err != nil || !strings.Contains(dl.header.Get("Content-Disposition"), ".dsse.json") {
		t.Fatalf("download: %d %v", dl.code, err)
	}
	jwks := rg.get("", "/.well-known/jwks.json")
	ks, err := pack.ParseJWKS([]byte(jwks.body))
	if err != nil {
		t.Fatal(err)
	}
	if res, err := pack.Verify(env, ks, nil); err != nil || !res.Verified {
		t.Errorf("the downloaded pack does not verify against the served JWKS: %+v %v", res, err)
	}

	// 6. Share it; the reviewer's verify page fetch works and the browser verifier accepts it; then revoke.
	r = rg.post(userA, sys+"/shares", url.Values{"verifier_org": {"Southern Cross Bank"}, "pack": {""}, "days": {"30"}})
	if r.code != 200 {
		t.Fatalf("create share: %d %s %s", r.code, r.location, r.body)
	}
	link := secret(t, r.body, "Share link")
	if !strings.HasPrefix(link, "https://verify.example/#t=") {
		t.Fatalf("share link %q", link)
	}
	shareToken := strings.TrimPrefix(link, "https://verify.example/#t=")
	v := rg.get("", "/v1/verify/"+shareToken)
	if v.code != 200 {
		t.Fatalf("verify API: %d", v.code)
	}
	if node, err := exec.LookPath("node"); err == nil {
		f := filepath.Join(t.TempDir(), "r.json")
		_ = os.WriteFile(f, []byte(v.body), 0o644)
		jf := filepath.Join(t.TempDir(), "jwks.json")
		_ = os.WriteFile(jf, []byte(jwks.body), 0o644)
		script := `import { readFileSync } from "node:fs"; import { verifyEnvelope } from "./web/verify/verify.js";
const b = JSON.parse(readFileSync(process.argv[1], "utf8")); const r = await verifyEnvelope(b.envelope, JSON.parse(readFileSync(process.argv[2], "utf8")), b.prev_envelope_digest);
console.log(r.verified ? "VERIFIED" : "FAILED " + r.signature_error);`
		cmd := exec.Command(node, "--input-type=module", "-e", script, f, jf)
		cmd.Dir = filepath.Join("..", "..")
		if out, err := cmd.CombinedOutput(); err != nil || !strings.HasPrefix(string(out), "VERIFIED") {
			t.Errorf("the reviewer's browser verifier: %v %s", err, out)
		}
	}
	shares := rg.get(userA, sys+"/shares")
	lid := regexp.MustCompile(`/shares/([0-9a-f-]{36})/revoke`).FindStringSubmatch(shares.body)
	if lid == nil || !strings.Contains(shares.body, "Southern Cross Bank") || !strings.Contains(shares.body, `<td class="num">1</td>`) {
		t.Fatalf("shares page does not show the link and its view:\n%s", shares.body)
	}
	if r := rg.post(userA, sys+"/shares/"+lid[1]+"/revoke", nil); r.code != 303 {
		t.Errorf("revoke: %d", r.code)
	}
	if v := rg.get("", "/v1/verify/"+shareToken); v.code != http.StatusGone {
		t.Errorf("after revoking: %d", v.code)
	}
	if p := rg.get(userA, "/settings"); !strings.Contains(p.body, "Wren Health") || !strings.Contains(p.body, "a@vendor.example") {
		t.Errorf("settings")
	}
}

func onboard(t *testing.T, rg *rig, user, org, system string) string {
	t.Helper()
	r := rg.post(user, "/onboarding", url.Values{"org": {org}, "system": {system}, "environment": {"production"}, "timezone": {"Australia/Sydney"}})
	if r.code != 303 {
		t.Fatalf("onboard %s: %d %s", org, r.code, r.body)
	}
	return strings.TrimSuffix(r.location, "?ok=welcome")
}

func TestTenantsCannotSeeEachOther(t *testing.T) {
	rg := newRig(t)
	sysA := onboard(t, rg, userA, "A Co", "A system")
	onboard(t, rg, userB, "B Co", "B system")
	for _, path := range []string{sysA, sysA + "/registers", sysA + "/packs", sysA + "/shares"} {
		if r := rg.get(userB, path); r.code != 404 {
			t.Errorf("B reading %s: %d", path, r.code)
		}
	}
	for _, path := range []string{sysA + "/enrolment-tokens", sysA + "/declarations", sysA + "/packs", sysA + "/shares"} {
		if r := rg.post(userB, path, url.Values{"models": {""}, "timezone": {"UTC"}, "prefix": {"PL"}, "verifier_org": {"x"}, "days": {"1"},
			"period_start": {"2026-09-01"}, "period_end": {"2026-09-01"}}); r.code != 404 {
			t.Errorf("B writing %s: %d", path, r.code)
		}
	}
	if r := rg.get(userB, "/systems"); strings.Contains(r.body, "A system") {
		t.Error("B lists A's system")
	}
	if r := rg.get(userA, "/systems/not-a-uuid"); r.code != 404 {
		t.Errorf("malformed id: %d", r.code)
	}
}

func TestFormsAreProtectedAndOutputEscaped(t *testing.T) {
	rg := newRig(t)
	sys := onboard(t, rg, userA, "A Co", `<script>alert(1)</script>`)
	// A form posted from another site, or with no Origin, is refused.
	if r := rg.do(userA, http.MethodPost, sys+"/enrolment-tokens", url.Values{}, false); r.code != 403 {
		t.Errorf("no Origin: %d", r.code)
	}
	req, _ := http.NewRequest(http.MethodPost, rg.srv.URL+sys+"/enrolment-tokens", nil)
	req.Header.Set("X-Test-User", userA)
	req.Header.Set("Origin", "https://evil.example")
	if r := rg.send(req); r.code != 403 {
		t.Errorf("another site: %d", r.code)
	}
	p := rg.get(userA, sys)
	if strings.Contains(p.body, "<script>alert(1)</script>") || !strings.Contains(p.body, "&lt;script&gt;") {
		t.Error("a system name was not escaped")
	}
	if csp := p.header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'none'") || p.header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("security headers: %v", p.header)
	}
	if r := rg.get("", sys); r.code != 303 || r.location != "/login" {
		t.Errorf("signed out: %d %s", r.code, r.location)
	}
	if r := rg.get("", "/login?error=link"); r.code != 200 || !strings.Contains(r.body, "expired or was already used") {
		t.Errorf("login page: %d", r.code)
	}
	if r := rg.get("", "/login?error=<b>x</b>"); strings.Contains(r.body, "<b>x</b>") {
		t.Error("the login page echoed the query")
	}
	if r := rg.get("", "/static/app.css"); r.code != 200 || !strings.Contains(r.header.Get("Content-Type"), "text/css") {
		t.Errorf("stylesheet: %d", r.code)
	}
}

func TestBadInputIsExplainedAndUploadsAreAllOrNothing(t *testing.T) {
	rg := newRig(t)
	sys := onboard(t, rg, userA, "A Co", "S")
	for name, c := range map[string]struct {
		path string
		form url.Values
		want string
	}{
		"model line":     {sys + "/declarations", url.Values{"models": {"just-a-model"}, "timezone": {"UTC"}, "prefix": {"PL"}}, "line 1"},
		"bad prefix":     {sys + "/declarations", url.Values{"models": {""}, "timezone": {"UTC"}, "prefix": {"lower"}}, "pack ID prefix"},
		"change kind":    {sys + "/registers/changes", url.Values{"date": {"2026-09-01"}, "kind": {"vibes"}, "what": {"x"}}, "kind must be one of"},
		"ack before":     {sys + "/registers/incidents", url.Values{"id": {"I-1"}, "opened": {"2026-09-01T10:00"}, "acknowledged": {"2026-09-01T09:00"}, "summary": {"x"}}, "cannot be before"},
		"pass rate":      {sys + "/registers/evals", url.Values{"suite": {"s"}, "model": {"m"}, "ran_at": {"2026-09-01T10:00"}, "pass_rate": {"1.5"}}, "from 0 to 1"},
		"period":         {sys + "/packs", url.Values{"period_start": {"2026-09-10"}, "period_end": {"2026-09-01"}}, "first and last day"},
		"share expiry":   {sys + "/shares", url.Values{"verifier_org": {"Bank"}, "days": {"0"}}, "1 to 365 days"},
		"share no org":   {sys + "/shares", url.Values{"verifier_org": {" "}, "days": {"5"}}, "Name the organisation"},
		"duplicate name": {"/systems", url.Values{"name": {"S"}, "environment": {"production"}, "timezone": {"UTC"}}, "already exists"},
	} {
		if r := rg.post(userA, c.path, c.form); r.code != 400 || !strings.Contains(r.body, c.want) {
			t.Errorf("%s: %d, want a message containing %q", name, r.code, c.want)
		}
	}
	r := rg.upload(userA, sys+"/registers/changes/upload", "date,kind,what\n2026-09-01,model,ok\n2026-09-02,vibes,bad\n")
	if r.code != 400 || !strings.Contains(r.body, "line 3") {
		t.Errorf("a bad row: %d", r.code)
	}
	if r := rg.upload(userA, sys+"/registers/changes/upload", "when,what\nx,y\n"); r.code != 400 || !strings.Contains(r.body, "must have the columns") {
		t.Errorf("bad header: %d", r.code)
	}
	var n int
	_ = rg.owner.QueryRow(context.Background(), `SELECT count(*) FROM register_changes`).Scan(&n)
	if n != 0 {
		t.Errorf("%d changes stored from rejected uploads", n)
	}
	if r := rg.upload(userA, sys+"/registers/evals/upload", "suite,model,prompt_version,ran_at,pass_rate\ns,m,14,2026-09-01T10:00:00Z,0.95\ns,m,15,2026-09-02T10:00:00Z,0.91\n"); !strings.Contains(r.body, "Imported 2 evals") {
		t.Errorf("good upload: %d", r.code)
	}
	// A failed issue is explained on the packs page.
	_ = rg.post(userA, sys+"/packs", url.Values{"period_start": {"2026-09-01"}, "period_end": {"2026-09-01"}})
	w := &jobs.Worker{Store: rg.st, Handlers: map[string]jobs.Handler{jobs.KindIssuePack: (&issue.Issuer{Store: rg.st, Signer: rg.signer}).Handler()}}
	_, _ = w.RunOnce(context.Background())
	if p := rg.get(userA, sys+"/packs"); !strings.Contains(p.body, "the agent sent no traces for that period") {
		t.Errorf("the failed issue is not explained")
	}
	_ = fmt.Sprint()
}
