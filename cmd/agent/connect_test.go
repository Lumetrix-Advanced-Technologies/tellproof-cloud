package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/server"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store/storetest"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// Acceptance (S-5): on a clean machine, `plumbline-agent connect <token>` plus
// the one-line SDK init gets a sample app's calls into a batch the server
// accepts, within seconds of the first call (here first_batch_seconds is 1;
// the default, 60, keeps it within the five minutes).
func TestConnectAndASampleAppReachTheServer(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	db := storetest.MigratedDB(t)
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(ctx)
	hash := sha256.Sum256([]byte("one-time-token-from-the-app"))
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, name) VALUES ('00000000-0000-0000-0000-00000000000a', 'A')`, nil},
		{`INSERT INTO ai_systems (id, tenant_id, name) VALUES ('30000000-0000-0000-0000-00000000000a', '00000000-0000-0000-0000-00000000000a', 'S')`, nil},
		{`INSERT INTO enrolment_tokens (tenant_id, ai_system_id, token_hash, expires_at) VALUES ('00000000-0000-0000-0000-00000000000a', '30000000-0000-0000-0000-00000000000a', $1, now() + interval '1 day')`, []any{hash[:]}},
	} {
		if _, err := owner.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	api := httptest.NewServer((&server.Server{Store: storetest.App(t, db), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler())
	defer api.Close()

	tickEvery = 200 * time.Millisecond
	defer func() { tickEvery = time.Minute }()
	listen := freePort(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	cfg := filepath.Join(t.TempDir(), "agent.json")
	_ = os.WriteFile(cfg, []byte(`{"otlp_listen": "`+listen+`", "aggregation": {"grace_minutes": 5, "first_batch_seconds": 1}}`), 0o600)
	getenv := func(k string) string {
		return map[string]string{"PLUMBLINE_SERVER": api.URL, "PLUMBLINE_DATA_DIR": dataDir}[k]
	}

	start := func(token string) (context.CancelFunc, chan int, *lockedBuffer) {
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan int, 1)
		out := &lockedBuffer{}
		go func() { done <- run(runCtx, []string{"connect", "--config", cfg, token}, getenv, out, out) }()
		for i := 0; i < 100; i++ {
			if c, err := net.Dial("tcp", listen); err == nil {
				c.Close()
				return cancel, done, out
			}
			select {
			case code := <-done:
				t.Fatalf("connect exited %d: %s", code, out.String())
			case <-time.After(50 * time.Millisecond):
			}
		}
		cancel()
		t.Fatalf("the agent did not start listening: %s", out.String())
		return nil, nil, nil
	}
	cancel, done, out := start("one-time-token-from-the-app")

	// The sample app: one line to init the SDK, then a model call.
	began := time.Now()
	app := `import plumbline
plumbline.init(agent_url="http://` + listen + `", service="sample-app")
with plumbline.prompt("triage", "1"), plumbline.llm_call("openai", "gpt-4.1-mini") as c:
    c.usage(120, 30)
assert plumbline.flush(5)
print(plumbline.stats())`
	cmd := exec.Command(py, "-c", app)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join("..", "..", "sdk", "python", "src"))
	if b, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(b), "'exported': 1") {
		t.Fatalf("sample app: %v %s", err, b)
	}
	var n int
	var calls int64
	for i := 0; i < 100 && n == 0; i++ {
		time.Sleep(100 * time.Millisecond)
		_ = owner.QueryRow(ctx, `SELECT count(*), coalesce(sum((payload->'metrics'->'guardrails'->>'llm_calls')::bigint), 0) FROM evidence_batches`).Scan(&n, &calls)
	}
	if n == 0 || calls != 1 {
		t.Fatalf("no batch from the sample app after %s (%d batches, %d calls): %s", time.Since(began), n, calls, out.String())
	}
	t.Logf("the sample app's call reached the server as a batch %s after it was made", time.Since(began).Round(time.Millisecond))
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("connect exit %d: %s", code, out.String())
	}
	if info, err := os.Stat(filepath.Join(dataDir, "salt.secret")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("salt secret: %v %v", info, err)
	}

	// The same command again (a restarted container): already enrolled, the used token is not needed.
	cancel, done, out = start("one-time-token-from-the-app")
	cancel()
	if code := <-done; code != 0 || !strings.Contains(out.String(), "already enrolled") {
		t.Errorf("restart: %d %s", code, out.String())
	}
	var agents int
	_ = owner.QueryRow(ctx, `SELECT count(*) FROM agents`).Scan(&agents)
	if agents != 1 {
		t.Errorf("%d agents after a restart", agents)
	}
}

func TestConnectUsage(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"connect"}, func(string) string { return "" }, &out, &out); code != 2 || !strings.Contains(out.String(), "PLUMBLINE_SERVER") {
		t.Errorf("no server: %d %s", code, out.String())
	}
	out.Reset()
	e := func(k string) string {
		return map[string]string{"PLUMBLINE_SERVER": "https://x.example", "PLUMBLINE_DATA_DIR": t.TempDir()}[k]
	}
	if code := run(context.Background(), []string{"connect"}, e, &out, &out); code != 2 || !strings.Contains(out.String(), "token") {
		t.Errorf("no token: %d %s", code, out.String())
	}
	if code := run(context.Background(), []string{"connect", "a", "b"}, e, &out, &out); code != 2 {
		t.Errorf("two tokens: %d", code)
	}
	_ = fmt.Sprint()
}
