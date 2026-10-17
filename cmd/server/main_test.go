package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/store"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/store/storetest"
	"github.com/lumetrix-advanced-technologies/witnessmark/migrations"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"frobnicate"}, {"migrate", "extra"}, {"migrate"}, {"serve"}, {"work"}} {
		var out, errb bytes.Buffer
		if code := run(context.Background(), args, env(), &out, &errb); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	var out bytes.Buffer
	if run(context.Background(), []string{"version"}, env(), &out, &out) != 0 || !strings.HasPrefix(out.String(), "witnessmark-server ") {
		t.Errorf("version: %q", out.String())
	}
}

func TestMigrateCommand(t *testing.T) {
	db := storetest.EmptyDB(t)
	ms, err := store.LoadMigrations(migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{fmt.Sprintf(`"applied":%d`, len(ms)), `"applied":0`} {
		var out, errb bytes.Buffer
		if code := run(context.Background(), []string{"migrate"}, env("DATABASE_URL_OWNER", db), &out, &errb); code != 0 {
			t.Fatalf("run %d: exit %d: %s", i, code, errb.String())
		}
		if !strings.Contains(errb.String(), want) {
			t.Errorf("run %d log: %s", i, errb.String())
		}
	}
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"migrate"}, env("DATABASE_URL_OWNER", "postgres://u:hunter2@127.0.0.1:1/x"), &out, &errb); code != 1 || strings.Contains(errb.String(), "hunter2") {
		t.Errorf("bad URL: exit %d, %s", code, errb.String())
	}
}

func TestServeAnswersAndStops(t *testing.T) {
	db := storetest.MigratedDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	var errb syncBuffer
	done := make(chan int)
	go func() {
		done <- run(ctx, []string{"serve"}, env("DATABASE_URL", db, "DATABASE_ROLE", "plumbline_app", "LISTEN", "127.0.0.1:0"), &bytes.Buffer{}, &errb)
	}()
	var addr string
	for i := 0; i < 100 && addr == ""; i++ {
		time.Sleep(20 * time.Millisecond)
		if m := listenRE.FindStringSubmatch(errb.String()); m != nil {
			addr = m[1]
		}
	}
	if addr == "" {
		cancel()
		t.Fatalf("server did not start: %s", errb.String())
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("healthz: %v %v", resp, err)
	}
	if resp != nil {
		resp.Body.Close()
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("serve exit %d: %s", code, errb.String())
	}
	if strings.Contains(errb.String(), "postgres://") {
		t.Error("the database URL was logged")
	}
}

var listenRE = regexp.MustCompile(`"listen":"([^"]+)"`)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestWorkSchedulesPruningAndStops(t *testing.T) {
	db := storetest.MigratedDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	var errb syncBuffer
	done := make(chan int)
	go func() {
		done <- run(ctx, []string{"work"}, env("DATABASE_URL", db, "DATABASE_ROLE", "plumbline_app"), &bytes.Buffer{}, &errb)
	}()
	for i := 0; i < 250 && !strings.Contains(errb.String(), `"job done"`); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("work exit %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), `"kind":"prune_jobs"`) {
		t.Errorf("the first prune did not run: %s", errb.String())
	}
	conn, err := pgx.Connect(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var next int
	_ = conn.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE kind = 'prune_jobs' AND done_at IS NULL AND run_at > now() + interval '23 hours'`).Scan(&next)
	if next != 1 {
		t.Errorf("%d prunes scheduled for tomorrow", next)
	}
}

func TestPackSignerConfig(t *testing.T) {
	if s, err := packSigner(context.Background(), env()); s != nil || err != nil {
		t.Errorf("unset: %v %v", s, err)
	}
	if _, err := packSigner(context.Background(), env("KMS_KEY_ID", "local", "LOCAL_SIGNING_KEY", "/nonexistent.pem")); err == nil {
		t.Error("a missing local key was accepted")
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	p := filepath.Join(t.TempDir(), "k.pem")
	_ = os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	s, err := packSigner(context.Background(), env("KMS_KEY_ID", "local", "LOCAL_SIGNING_KEY", p))
	if err != nil || s == nil || !strings.HasPrefix(s.KeyRef(), "local:") {
		t.Errorf("local: %v %v", s, err)
	}
}

func TestKeysCommand(t *testing.T) {
	db := storetest.MigratedDB(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	p := filepath.Join(t.TempDir(), "k.pem")
	_ = os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	e := env("DATABASE_URL_OWNER", db, "KMS_KEY_ID", "local", "LOCAL_SIGNING_KEY", p)
	do := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := run(context.Background(), append([]string{"keys"}, args...), e, &out, &errb)
		return code, out.String() + errb.String()
	}
	code, out := do("add", "--nbf", "2026-01-01T00:00:00Z")
	if code != 0 || !strings.Contains(out, "added ") {
		t.Fatalf("add: %d %s", code, out)
	}
	kid := strings.Fields(out)[1]
	if code, out := do("add"); code != 1 {
		t.Errorf("adding the same key twice: %d %s", code, out)
	}
	if code, out := do("retire", kid, "--at", "2026-06-01T00:00:00Z"); code != 0 {
		t.Errorf("retire: %d %s", code, out)
	}
	if code, out := do("revoke", kid); code != 1 || !strings.Contains(out, "--reason") {
		t.Errorf("revoke without a reason: %d %s", code, out)
	}
	if code, out := do("revoke", kid, "--reason", "rotation drill"); code != 0 {
		t.Errorf("revoke: %d %s", code, out)
	}
	if code, out := do("list"); code != 0 || !strings.Contains(out, kid) || !strings.Contains(out, "revoked") {
		t.Errorf("list: %d %s", code, out)
	}
	for _, args := range [][]string{{}, {"list", "extra"}, {"frobnicate"}} {
		if code, _ := do(args...); code == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
}
