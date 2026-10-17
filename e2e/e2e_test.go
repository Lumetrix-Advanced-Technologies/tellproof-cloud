//go:build e2e

// Package e2e runs the whole product end to end (S-6): a sample Python app and
// a sample Node.js app, each with the one-line SDK init, send spans to an
// enrolled agent; the agent delivers signed batches to the server; the server
// evaluates the period and issues a signed pack; a share link opens the
// verify page in headless Chrome, which verifies the pack in the browser.
//
//	WITNESSMARK_TEST_DATABASE_URL=... go test -tags e2e ./e2e/ -v
package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/agent"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/dsse"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/enrol"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/issue"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/keys"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/kmssign"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/pack"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/server"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/share"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/store/storetest"
)

const (
	tenant = "00000000-0000-0000-0000-00000000000a"
	system = "30000000-0000-0000-0000-00000000000a"
	member = "10000000-0000-0000-0000-00000000000a"
	calls  = 20
)

var root = filepath.Join("..")

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func need(t *testing.T, names ...string) string {
	t.Helper()
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	if os.Getenv("CI") != "" {
		t.Fatalf("%s is required in CI", names[0])
	}
	t.Skipf("%s not installed", names[0])
	return ""
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestEndToEnd(t *testing.T) {
	py, node, chrome := need(t, "python3"), need(t, "node"), need(t, "google-chrome", "google-chrome-stable", "chromium", "chromium-browser")
	ctx := context.Background()
	db := storetest.MigratedDB(t)
	owner, err := pgx.Connect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(ctx)

	// A vendor with one system and its declarations, and a one-time enrolment token.
	token := "e2e-enrolment-token-0123456789"
	th := sha256.Sum256([]byte(token))
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, name) VALUES ($1, 'Wren Health (e2e)')`, []any{tenant}},
		{`INSERT INTO members (id, tenant_id, user_id, email) VALUES ($1, $2, '20000000-0000-0000-0000-00000000000a', 'e2e@vendor.example')`, []any{member, tenant}},
		{`INSERT INTO ai_systems (id, tenant_id, name, timezone, pack_prefix, declared_models, declared_regions) VALUES ($1, $2, 'Claims triage (e2e)', 'UTC', 'E2E',
		  '[{"provider":"openai","model":"gpt-4.1-mini"},{"provider":"anthropic","model":"claude-sonnet-4-5"}]', '["us-east-1"]')`, []any{system, tenant}},
		{`INSERT INTO enrolment_tokens (tenant_id, ai_system_id, token_hash, expires_at) VALUES ($1, $2, $3, now() + interval '1 hour')`, []any{tenant, system, th[:]}},
	} {
		if _, err := owner.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	signer, _ := kmssign.NewLocal(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err := pgx.BeginFunc(ctx, owner, func(tx pgx.Tx) error { return keys.Add(ctx, tx, signer, time.Now().Add(-time.Hour)) }); err != nil {
		t.Fatal(err)
	}

	// The server, with the static verify page on the same origin (as a deploy may host it).
	st := storetest.App(t, db)
	api := (&server.Server{Store: st, Log: quiet(), ViewSecret: []byte("e2e-view-secret-0123456789")}).Handler()
	mux := http.NewServeMux()
	mux.Handle("/verify/", http.StripPrefix("/verify/", http.FileServer(http.Dir(filepath.Join(root, "web", "verify")))))
	mux.HandleFunc("/brand.css", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(root, "web", "brand.css"))
	})
	mux.Handle("/", api)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// The agent: enrol with the token, receive on a local port, deliver to the server.
	dir := filepath.Join(t.TempDir(), "agent")
	if _, err := enrol.Enrol(ctx, enrol.Options{Server: srv.URL, Token: token, DataDir: dir, HTTP: srv.Client()}); err != nil {
		t.Fatal(err)
	}
	listen := freeAddr(t)
	cfg, _ := agent.LoadConfig("", func(string) string { return "" })
	cfg.Server, cfg.DataDir, cfg.OTLPListen = srv.URL, dir, listen
	ag, err := agent.New(cfg, agent.Options{Log: quiet(), HTTP: srv.Client(), TickEvery: time.Second,
		Getenv: func(k string) string {
			return map[string]string{"WITNESSMARK_SALT_SECRET": "e2e-salt-secret-0123456789"}[k]
		}})
	if err != nil {
		t.Fatal(err)
	}
	agentCtx, stopAgent := context.WithCancel(ctx)
	agentDone := make(chan error, 1)
	go func() { agentDone <- ag.Run(agentCtx) }()
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", listen); err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The two sample apps.
	env := append(os.Environ(), "WITNESSMARK_AGENT_URL=http://"+listen, "SAMPLE_CALLS="+itoa(calls), "PYTHONPATH="+filepath.Join(root, "sdk", "python", "src"))
	for _, app := range [][]string{{py, filepath.Join(root, "examples", "python-app", "app.py")}, {node, filepath.Join(root, "examples", "node-app", "app.mjs")}} {
		cmd := exec.Command(app[0], app[1])
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", filepath.Base(app[1]), err, out)
		}
		t.Logf("%s", strings.TrimSpace(string(out)))
	}
	stopAgent() // shuts down gracefully: sends the open hour and delivers
	if err := <-agentDone; err != nil {
		t.Fatalf("agent: %v", err)
	}
	var batches int
	var llm int64
	_ = owner.QueryRow(ctx, `SELECT count(*), coalesce(sum((payload->'metrics'->'guardrails'->>'llm_calls')::bigint), 0) FROM evidence_batches`).Scan(&batches, &llm)
	// Python: 20 triage calls + 4 agent runs with one call each; Node: 20 calls.
	if want := int64(calls + calls/5 + calls); batches == 0 || llm != want {
		t.Fatalf("the server holds %d batches with %d LLM calls, want %d", batches, llm, want)
	}

	// The server evaluates the period and issues a signed pack.
	today := time.Now().UTC()
	issued, err := (&issue.Issuer{Store: st, Signer: signer}).Issue(ctx, issue.Request{TenantID: tenant, SystemID: system,
		PeriodStart: today.AddDate(0, 0, -1).Format("2006-01-02"), PeriodEnd: today.Format("2006-01-02")})
	if err != nil {
		t.Fatal(err)
	}
	env2, _ := dsse.Decode(issued.Envelope)
	stmt, _ := pack.DecodeStatement(env2.Payload)
	status := map[string]string{}
	for _, o := range stmt.Predicate.Objectives {
		status[o.ID] = o.Status
	}
	t.Logf("pack %s: %v", issued.PackID, status)
	// Pass: what the samples do right. EO-LOG-01 and EO-EVAL-01 need provider usage and eval runs, which the samples do not have.
	for _, id := range []string{"EO-LIN-01", "EO-RES-01", "EO-PII-01", "EO-CNT-01", "EO-GRD-01", "EO-CHG-01", "EO-INC-01", "EO-AGT-01"} {
		if status[id] != "pass" {
			t.Errorf("%s = %s, want pass for the sample apps' traffic", id, status[id])
		}
	}

	// A share link, opened in a real browser: the verify page fetches the pack and the keys and verifies them.
	var link *share.Link
	if err := st.WithTenant(ctx, tenant, func(tx pgx.Tx) error {
		var err error
		link, err = share.Create(ctx, tx, member, share.Target{SystemID: system}, "E2E Bank", time.Now().Add(24*time.Hour), time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	profile := t.TempDir()
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--no-first-run", "--user-data-dir="+profile,
		"--virtual-time-budget=20000", "--dump-dom", srv.URL+"/verify/index.html?t="+link.Token)
	dom, err := cmd.Output()
	if err != nil {
		t.Fatalf("chrome: %v", err)
	}
	page := string(dom)
	if !strings.Contains(page, "Verified, unaltered since signing") || !strings.Contains(page, "Claims triage (e2e)") || !strings.Contains(page, issued.PackID) {
		i := strings.Index(page, "<main")
		t.Fatalf("the verify page did not verify the pack:\n%s", page[max(i, 0):min(len(page), max(i, 0)+4000)])
	}
	var views int
	_ = owner.QueryRow(ctx, `SELECT count(*) FROM verifier_views`).Scan(&views)
	if views != 1 {
		t.Errorf("%d views logged", views)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
