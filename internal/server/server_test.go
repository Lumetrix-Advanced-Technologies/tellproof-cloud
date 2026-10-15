package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/agent"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/aggregate"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/batch"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/batchsign"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/dsse"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/enrol"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/otlp"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store/storetest"
)

const (
	tenantA = "00000000-0000-0000-0000-00000000000a"
	tenantB = "00000000-0000-0000-0000-00000000000b"
	systemA = "30000000-0000-0000-0000-00000000000a"
)

type fixture struct {
	srv   *httptest.Server
	owner *pgx.Conn
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newFixture: a migrated database with tenants A and B, one system for A and
// enrolment tokens (tok-1 … tok-5 valid, tok-expired expired), and the API on it.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := storetest.MigratedDB(t)
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close(context.Background()) })
	for _, q := range []string{
		`INSERT INTO tenants (id, name) VALUES ('` + tenantA + `', 'A'), ('` + tenantB + `', 'B')`,
		`INSERT INTO ai_systems (id, tenant_id, name) VALUES ('` + systemA + `', '` + tenantA + `', 'Claims Agent')`,
	} {
		if _, err := owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, tok := range []string{"tok-1", "tok-2", "tok-3", "tok-4", "tok-5", "tok-expired"} {
		h := sha256.Sum256([]byte(tok))
		exp := time.Now().Add(time.Hour)
		if tok == "tok-expired" {
			exp = time.Now().Add(-time.Minute)
		}
		if _, err := owner.Exec(ctx, `INSERT INTO enrolment_tokens (tenant_id, ai_system_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
			tenantA, systemA, h[:], exp); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Store: storetest.App(t, db), Log: quiet()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &fixture{srv: srv, owner: owner}
}

func (f *fixture) enrol(t *testing.T, token string) *enrol.Identity {
	t.Helper()
	id, err := enrol.Enrol(context.Background(), enrol.Options{Server: f.srv.URL, Token: token, DataDir: filepath.Join(t.TempDir(), "agent"), HTTP: f.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) post(t *testing.T, body []byte) (int, string) {
	t.Helper()
	resp, err := f.srv.Client().Post(f.srv.URL+"/v1/batches", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Digest string `json:"digest"`
		Error  string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Digest
}

func encode(t *testing.T, env *dsse.Envelope) []byte {
	t.Helper()
	b, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// window is one aggregated hour (one LLM call) starting at 2026-09-01 hh:00 UTC.
func window(t *testing.T, hh int) aggregate.Window {
	t.Helper()
	start := time.Date(2026, 9, 1, hh, 0, 0, 0, time.UTC)
	a := aggregate.New(aggregate.DefaultConfig())
	a.Add([]otlp.Span{{TraceID: strings.Repeat("ab", 16), Start: start.Add(5 * time.Minute), Kind: otlp.KindLLM, Provider: "openai",
		Model: "gpt-4.1-mini", Region: "unknown", InputTokens: 10, OutputTokens: 2}}, start.Add(10*time.Minute))
	return a.FlushAll()[0]
}

func build(t *testing.T, id *enrol.Identity, st batchsign.State, w aggregate.Window) (*dsse.Envelope, batchsign.State) {
	t.Helper()
	b, err := batchsign.NewBuilder(id, "v2")
	if err != nil {
		t.Fatal(err)
	}
	env, _, next, err := b.Build(context.Background(), st, w, batch.Commit{Root: strings.Repeat("0", 64), Leaves: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return env, next
}

func TestEnrol(t *testing.T) {
	f := newFixture(t)
	id := f.enrol(t, "tok-1")
	var key []byte
	var system string
	if err := f.owner.QueryRow(context.Background(), `SELECT public_key, ai_system_id FROM agents WHERE id = $1`, id.AgentID).Scan(&key, &system); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, id.Key.Public().(ed25519.PublicKey)) || system != systemA {
		t.Errorf("stored agent: key match %v, system %s", bytes.Equal(key, id.Key.Public().(ed25519.PublicKey)), system)
	}

	call := func(auth string, body string) int {
		req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/v1/agents/enrol", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := f.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	good := `{"public_key":` + jsonString(pemOf(t, pub)) + `,"hostname":"h"}`
	for name, c := range map[string]struct {
		auth, body string
		want       int
	}{
		"used token":         {"Bearer tok-1", good, 401},
		"expired token":      {"Bearer tok-expired", good, 401},
		"unknown token":      {"Bearer nope", good, 401},
		"no token":           {"", good, 401},
		"not a key":          {"Bearer tok-2", `{"public_key":"x","hostname":"h"}`, 400},
		"unknown field":      {"Bearer tok-2", `{"public_key":"x","extra":1}`, 400},
		"P-256, not Ed25519": {"Bearer tok-2", `{"public_key":` + jsonString(p256PEM) + `}`, 400},
	} {
		if got := call(c.auth, c.body); got != c.want {
			t.Errorf("%s: HTTP %d, want %d", name, got, c.want)
		}
	}
	// A refused request never spends the token.
	if got := call("Bearer tok-2", good); got != 201 {
		t.Errorf("tok-2 after refusals: HTTP %d", got)
	}
}

// Acceptance: valid batches return 201; bad signature 401; seq gap 409;
// duplicate seq returns the stored digest (idempotent).
func TestIngest(t *testing.T) {
	f := newFixture(t)
	id := f.enrol(t, "tok-1")
	e1, s1 := build(t, id, batchsign.State{}, window(t, 10))
	e2, s2 := build(t, id, s1, window(t, 11))
	d1, d2 := batchsign.Digest(e1), batchsign.Digest(e2)

	if code, d := f.post(t, encode(t, e1)); code != 201 || d != d1 {
		t.Fatalf("batch 1: %d %s", code, d)
	}
	if code, d := f.post(t, encode(t, e2)); code != 201 || d != d2 {
		t.Fatalf("batch 2: %d %s", code, d)
	}
	if code, d := f.post(t, encode(t, e1)); code != 200 || d != d1 {
		t.Errorf("resent batch 1: %d %s, want 200 %s", code, d, d1)
	}
	other, _ := build(t, id, batchsign.State{}, window(t, 12)) // a different batch under seq 1
	if code, d := f.post(t, encode(t, other)); code != 200 || d != d1 {
		t.Errorf("conflicting seq 1: %d %s, want 200 with the stored digest %s", code, d, d1)
	}
	gap, _ := build(t, id, batchsign.State{Seq: 3, Prev: s2.Prev}, window(t, 13))
	if code, _ := f.post(t, encode(t, gap)); code != 409 {
		t.Errorf("seq gap: %d", code)
	}
	bogus := "sha256:" + strings.Repeat("0", 64)
	wrongPrev, _ := build(t, id, batchsign.State{Seq: 2, Prev: &bogus}, window(t, 13))
	if code, _ := f.post(t, encode(t, wrongPrev)); code != 409 {
		t.Errorf("wrong prev: %d", code)
	}

	// Signed by another key under this agent's ID.
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	forged, _ := build(t, &enrol.Identity{AgentID: id.AgentID, Key: otherKey}, s2, window(t, 13))
	if code, _ := f.post(t, encode(t, forged)); code != 401 {
		t.Errorf("bad signature: %d", code)
	}
	tampered, _ := build(t, id, s2, window(t, 13))
	tampered.Payload = bytes.Replace(tampered.Payload, []byte(`"input_tokens":10`), []byte(`"input_tokens":99`), 1)
	if code, _ := f.post(t, encode(t, tampered)); code != 401 {
		t.Errorf("tampered payload: %d", code)
	}
	stranger, _ := build(t, &enrol.Identity{AgentID: "agt_unknown000000", Key: otherKey}, batchsign.State{}, window(t, 13))
	if code, _ := f.post(t, encode(t, stranger)); code != 401 {
		t.Errorf("unknown agent: %d", code)
	}
	signer, _ := dsse.NewEd25519Signer(id.Key, id.AgentID)
	loose, _ := dsse.Sign(context.Background(), signer, batch.PayloadType, []byte(`{ "schema": "plumbline-batch/v0.2" }`))
	if code, _ := f.post(t, encode(t, loose)); code != 400 {
		t.Errorf("non-canonical, invalid payload: %d", code)
	}
	if code, _ := f.post(t, []byte(`{"not":"an envelope"}`)); code != 400 {
		t.Errorf("garbage: %d", code)
	}
	if code, _ := f.post(t, bytes.Repeat([]byte(" "), MaxBatchBytes+1)); code != 413 {
		t.Errorf("too large: %d", code)
	}

	var n, lastSeq int64
	var lastDigest, tenant string
	ctx := context.Background()
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM evidence_batches`).Scan(&n)
	_ = f.owner.QueryRow(ctx, `SELECT last_seq, last_digest FROM agents WHERE id = $1`, id.AgentID).Scan(&lastSeq, &lastDigest)
	_ = f.owner.QueryRow(ctx, `SELECT DISTINCT tenant_id::text FROM evidence_batches`).Scan(&tenant)
	if n != 2 || lastSeq != 2 || lastDigest != d2 || tenant != tenantA {
		t.Errorf("stored %d batches, last_seq %d, last_digest %s, tenant %s", n, lastSeq, lastDigest, tenant)
	}
	var envelope string
	_ = f.owner.QueryRow(ctx, `SELECT envelope::text FROM evidence_batches WHERE seq = 1`).Scan(&envelope)
	if back, err := dsse.Decode([]byte(envelope)); err != nil || batchsign.Digest(back) != d1 {
		t.Errorf("the stored envelope does not verify back to its digest: %v", err)
	}

	// The chain continues after the refusals.
	e3, _ := build(t, id, s2, window(t, 13))
	if code, _ := f.post(t, encode(t, e3)); code != 201 {
		t.Errorf("batch 3: %d", code)
	}
}

func TestRevokedAgentIsRefused(t *testing.T) {
	f := newFixture(t)
	id := f.enrol(t, "tok-1")
	if _, err := f.owner.Exec(context.Background(), `UPDATE agents SET revoked_at = now() WHERE id = $1`, id.AgentID); err != nil {
		t.Fatal(err)
	}
	e, _ := build(t, id, batchsign.State{}, window(t, 10))
	if code, _ := f.post(t, encode(t, e)); code != 401 {
		t.Errorf("revoked agent: %d", code)
	}
}

// The same next batch sent many times at once is stored once.
func TestConcurrentResends(t *testing.T) {
	f := newFixture(t)
	id := f.enrol(t, "tok-1")
	e, _ := build(t, id, batchsign.State{}, window(t, 10))
	body := encode(t, e)
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, d := f.post(t, body)
			if d != batchsign.Digest(e) {
				t.Errorf("digest %s", d)
			}
			mu.Lock()
			codes[code]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	var n int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM evidence_batches`).Scan(&n)
	if codes[201] != 1 || codes[200] != 19 || n != 1 {
		t.Errorf("codes %v, rows %d", codes, n)
	}
}

// The real agent enrols, replays the sample and delivers to the real API.
func TestAgentEndToEnd(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "agent")
	if _, err := enrol.Enrol(context.Background(), enrol.Options{Server: f.srv.URL, Token: "tok-1", DataDir: dir, HTTP: f.srv.Client()}); err != nil {
		t.Fatal(err)
	}
	cfg, err := agent.LoadConfig("", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server, cfg.DataDir = f.srv.URL, dir
	a, err := agent.New(cfg, agent.Options{Log: quiet(), HTTP: f.srv.Client(),
		Getenv: func(k string) string { return map[string]string{"PLUMBLINE_SALT_SECRET": "a-salt-secret-0123456"}[k] }})
	if err != nil {
		t.Fatal(err)
	}
	sample, err := os.Open(filepath.Join("..", "..", "concierge", "testdata", "otlp-sample.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer sample.Close()
	if err := a.Replay(sample); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Deliver(ctx); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	var n int
	var leaves int64
	_ = f.owner.QueryRow(ctx, `SELECT count(*), sum((payload->'commit'->>'leaves')::bigint) FROM evidence_batches WHERE agent_id = $1`, a.AgentID()).Scan(&n, &leaves)
	if n != 2 || leaves == 0 {
		t.Errorf("stored %d batches with %d leaves", n, leaves)
	}
	var calls int64
	_ = f.owner.QueryRow(ctx, `SELECT sum((payload->'metrics'->'guardrails'->>'llm_calls')::bigint) FROM evidence_batches`).Scan(&calls)
	if calls != 320 {
		t.Errorf("stored metrics count %d LLM calls", calls)
	}
}

func pemOf(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	b, err := marshalPKIX(pub)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// p256PEM is a P-256 public key (spec/vectors/test-public-key.pem's kind): not an agent key.
var p256PEM = func() string {
	b, err := os.ReadFile(filepath.Join("..", "..", "spec", "vectors", "test-public-key.pem"))
	if err != nil {
		panic(err)
	}
	return string(b)
}()
