package main

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store/storetest"
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
	if run(context.Background(), []string{"version"}, env(), &out, &out) != 0 || !strings.HasPrefix(out.String(), "plumbline-server ") {
		t.Errorf("version: %q", out.String())
	}
}

func TestMigrateCommand(t *testing.T) {
	db := storetest.EmptyDB(t)
	for i, want := range []string{`"applied":1`, `"applied":0`} {
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
