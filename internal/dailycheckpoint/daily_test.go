package dailycheckpoint

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/checkpoint"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/dsse"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/jobs"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/keys"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/kmssign"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/pack"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/store/storetest"
)

// fakeGitHub is the contents and commits API over an in-memory repository.
type fakeGitHub struct {
	mu      sync.Mutex
	files   map[string][]byte
	commits map[string]string
	auth    []string
	fail    bool
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.auth = append(g.auth, r.Header.Get("Authorization"))
	if g.fail {
		w.WriteHeader(502)
		return
	}
	const prefix = "/repos/lumetrix/checkpoints/contents/"
	switch {
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, prefix):
		path := strings.TrimPrefix(r.URL.Path, prefix)
		if _, ok := g.files[path]; ok {
			w.WriteHeader(422)
			return
		}
		var body struct{ Message, Content string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		b, _ := base64.StdEncoding.DecodeString(body.Content)
		g.files[path] = b
		sha := fmt.Sprintf("%040d", len(g.files))
		g.commits[path] = sha
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": sha}})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, prefix):
		b, ok := g.files[strings.TrimPrefix(r.URL.Path, prefix)]
		if !ok {
			w.WriteHeader(404)
			return
		}
		enc := base64.StdEncoding.EncodeToString(b)
		// GitHub wraps content at 60 characters.
		var wrapped strings.Builder
		for i := 0; i < len(enc); i += 60 {
			wrapped.WriteString(enc[i:min(i+60, len(enc))] + "\n")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"content": wrapped.String(), "encoding": "base64"})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/lumetrix/checkpoints/commits":
		_ = json.NewEncoder(w).Encode([]map[string]string{{"sha": g.commits[r.URL.Query().Get("path")]}})
	default:
		w.WriteHeader(404)
	}
}

func newGitHub(t *testing.T) (*fakeGitHub, *GitHub) {
	t.Helper()
	g := &fakeGitHub{files: map[string][]byte{}, commits: map[string]string{}}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return g, &GitHub{Repo: "lumetrix/checkpoints", Token: "ghp_secret_token", BaseURL: srv.URL, HTTP: srv.Client()}
}

func TestGitHubNeverOverwrites(t *testing.T) {
	g, gh := newGitHub(t)
	ctx := context.Background()
	sha, err := gh.Publish(ctx, "checkpoints/2026/10/01.json", []byte("one\n"), "m")
	if err != nil || sha == "" {
		t.Fatalf("publish: %q %v", sha, err)
	}
	again, err := gh.Publish(ctx, "checkpoints/2026/10/01.json", []byte("one\n"), "m") // a retry after a crash
	if err != nil || again != sha {
		t.Errorf("the same file again: %q %v, want %q", again, err, sha)
	}
	if _, err := gh.Publish(ctx, "checkpoints/2026/10/01.json", []byte("two\n"), "m"); err == nil || !strings.Contains(err.Error(), "never overwritten") {
		t.Errorf("different bytes over a published file: %v", err)
	}
	g.fail = true
	if _, err := gh.Publish(ctx, "checkpoints/2026/10/02.json", []byte("x"), "m"); err == nil || strings.Contains(err.Error(), "ghp_secret_token") {
		t.Errorf("a failing API: %v", err)
	}
	if g.auth[0] != "Bearer ghp_secret_token" {
		t.Errorf("auth header %q", g.auth[0])
	}
}

type fixture struct {
	owner  *pgx.Conn
	job    *Job
	signer *kmssign.Local
	clock  time.Time
}

func newFixture(t *testing.T, pub Publisher) *fixture {
	t.Helper()
	db := storetest.MigratedDB(t)
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close(context.Background()) })
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	s, _ := kmssign.NewLocal(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err := pgx.BeginFunc(ctx, owner, func(tx pgx.Tx) error {
		return keys.Add(ctx, tx, s, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{owner: owner, signer: s}
	f.job = &Job{Store: storetest.App(t, db), Signer: s, Publisher: pub, Now: func() time.Time { return f.clock }}
	for _, q := range []string{
		`INSERT INTO tenants (id, name) VALUES ('00000000-0000-0000-0000-00000000000a', 'A'), ('00000000-0000-0000-0000-00000000000b', 'B')`,
		`INSERT INTO ai_systems (id, tenant_id, name) VALUES ('30000000-0000-0000-0000-00000000000a', '00000000-0000-0000-0000-00000000000a', 'S'),
		   ('30000000-0000-0000-0000-00000000000b', '00000000-0000-0000-0000-00000000000b', 'T')`,
		`INSERT INTO evaluations (id, tenant_id, ai_system_id, period_start, period_end, objective_set_hash) VALUES
		   ('40000000-0000-0000-0000-00000000000a', '00000000-0000-0000-0000-00000000000a', '30000000-0000-0000-0000-00000000000a', '2026-09-01', '2026-10-01', 'x'),
		   ('40000000-0000-0000-0000-00000000000b', '00000000-0000-0000-0000-00000000000b', '30000000-0000-0000-0000-00000000000b', '2026-09-01', '2026-10-01', 'x')`,
	} {
		if _, err := owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// storePack inserts one of the spec's pass vectors as a pack issued at at,
// for tenant a or b, and returns its envelope file and digest.
func (f *fixture) storePack(t *testing.T, vector string, seq int, tenant string, at time.Time) (string, string) {
	t.Helper()
	file := filepath.Join("..", "..", "spec", "vectors", "pass", vector)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dsse.Decode(raw)
	d := pack.Digest(env)
	var prev *string
	if seq > 1 {
		p := "sha256:" + strings.Repeat(fmt.Sprint(seq-1), 64)[:64]
		prev = &p
	}
	if _, err := f.owner.Exec(context.Background(), `INSERT INTO packs (tenant_id, ai_system_id, evaluation_id, pack_id, seq_no, prev_digest, digest, envelope, kms_key_id, issued_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'k', $9)`,
		tenant, "3"+tenant[1:], "4"+tenant[1:], fmt.Sprint("KST-", seq), seq, prev, d, string(raw), at); err != nil {
		t.Fatal(err)
	}
	return file, d
}

const tenantA, tenantB = "00000000-0000-0000-0000-00000000000a", "00000000-0000-0000-0000-00000000000b"

func syd(y int, m time.Month, d, h, mi int) time.Time {
	loc, _ := time.LoadLocation("Australia/Sydney")
	return time.Date(y, m, d, h, mi, 0, 0, loc)
}

// Acceptance (P4-7): a script proves a given pack is in that day's checkpoint.
func TestDailyCheckpointsAndTheProofScript(t *testing.T) {
	g, gh := newGitHub(t)
	f := newFixture(t, gh)
	ctx := context.Background()
	// Day 1 (Sydney): two packs from two tenants, one late in the evening; day 2: none; day 3: one.
	_, d1a := f.storePack(t, "KST-CTA-0001.dsse.json", 1, tenantA, syd(2026, 10, 1, 9, 0))
	file1b, d1b := f.storePack(t, "KST-CTA-0002.dsse.json", 2, tenantB, syd(2026, 10, 1, 23, 59))
	file3, d3 := f.storePack(t, "KST-CTA-0003.dsse.json", 3, tenantA, syd(2026, 10, 3, 0, 1))

	f.clock = syd(2026, 10, 4, 0, 10)
	if err := f.job.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.owner.Query(ctx, `SELECT day::text, pack_count, git_commit, envelope::text FROM checkpoints ORDER BY day`)
	type stored struct {
		day    string
		count  int
		commit *string
		env    string
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (stored, error) {
		var s stored
		return s, r.Scan(&s.day, &s.count, &s.commit, &s.env)
	})
	if err != nil || len(got) != 3 {
		t.Fatalf("checkpoints %+v (%v)", got, err)
	}
	wantDays := []string{"2026-10-01", "2026-10-02", "2026-10-03"}
	wantCounts := []int{2, 0, 1}
	jwks := filepath.Join(t.TempDir(), "jwks.json")
	_ = os.WriteFile(jwks, kmssign.JWKS(f.signer.PublicKey(), f.signer.KeyID()), 0o644)
	ks, _ := pack.ParseJWKS(kmssign.JWKS(f.signer.PublicKey(), f.signer.KeyID()))
	var prev *dsse.Envelope
	for i, s := range got {
		env, _ := dsse.Decode([]byte(s.env))
		p, err := checkpoint.Verify(env, ks, prev)
		if err != nil || s.day != wantDays[i] || s.count != wantCounts[i] || s.commit == nil {
			t.Errorf("checkpoint %d: %+v %v", i, s, err)
			continue
		}
		path := "checkpoints/" + strings.ReplaceAll(s.day, "-", "/") + ".json"
		if published, ok := g.files[path]; !ok || !bytes.Contains(published, []byte(env.Signatures[0].KeyID)) {
			t.Errorf("%s was not published", path)
		}
		if i == 0 && (p.PackDigests[0] != d1a || p.PackDigests[1] != d1b) {
			t.Errorf("day 1 lists %v, want issue order %s %s", p.PackDigests, d1a, d1b)
		}
		prev = env
	}
	if again := f.job.CatchUp(ctx); again != nil {
		t.Errorf("a second run: %v", again)
	}

	// The script: witnessmark-verify checkpoint proves pack 2 is in day 1's published checkpoint and pack 3 is not.
	dir := t.TempDir()
	day1 := filepath.Join(dir, "01.json")
	day3 := filepath.Join(dir, "03.json")
	_ = os.WriteFile(day1, g.files["checkpoints/2026/10/01.json"], 0o644)
	_ = os.WriteFile(day3, g.files["checkpoints/2026/10/03.json"], 0o644)
	day2 := filepath.Join(dir, "02.json")
	_ = os.WriteFile(day2, g.files["checkpoints/2026/10/02.json"], 0o644)
	run := func(args ...string) (int, string) {
		cmd := exec.Command("go", append([]string{"run", "./cmd/verify", "checkpoint", "--jwks", jwks}, args...)...)
		cmd.Dir = filepath.Join("..", "..")
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return code, string(out)
	}
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	if code, out := run(day1, abs(file1b)); code != 0 || !strings.Contains(out, "is pack 2 of 2 on 2026-10-01") || !strings.Contains(out, "RESULT: VERIFIED") {
		t.Errorf("proof of pack 2 in day 1: exit %d\n%s", code, out)
	}
	if code, out := run(day1, abs(file3)); code != 1 || !strings.Contains(out, "is not in 2026-10-01") {
		t.Errorf("pack 3 against day 1: exit %d\n%s", code, out)
	}
	if code, out := run("--prev", day2, day3, abs(file3)); code != 0 || !strings.Contains(out, "previous day ......... linked") {
		t.Errorf("day 3 linked to day 2, with pack 3: exit %d\n%s", code, out)
	}
	if code, out := run("--prev", day1, day3); code != 1 {
		t.Errorf("day 3 against day 1 as its previous day: exit %d\n%s", code, out)
	}
	_ = d3
}

func TestTheJobSchedulesTheNextRunAndWaitsForTheFirstPack(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	f.clock = syd(2026, 10, 4, 0, 10)
	if err := f.job.CatchUp(ctx); err != nil { // no packs yet: nothing to checkpoint
		t.Fatal(err)
	}
	var n int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM checkpoints`).Scan(&n)
	if n != 0 {
		t.Errorf("%d checkpoints before any pack", n)
	}
	f.storePack(t, "KST-CTA-0001.dsse.json", 1, tenantA, syd(2026, 10, 4, 9, 0))
	f.clock = syd(2026, 10, 5, 0, 10)
	if got := f.job.NextRun(); !got.Equal(syd(2026, 10, 6, 0, 10)) {
		t.Errorf("next run %v", got)
	}
	f.clock = syd(2026, 10, 5, 0, 5)
	if got := f.job.NextRun(); !got.Equal(syd(2026, 10, 5, 0, 10)) {
		t.Errorf("next run before 00:10: %v", got)
	}
	w := &jobs.Worker{Store: f.job.Store, Handlers: map[string]jobs.Handler{jobs.KindCheckpoint: f.job.Handler()}}
	if err := jobs.EnsureScheduled(ctx, f.job.Store, jobs.KindCheckpoint, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if found, err := w.RunOnce(ctx); !found || err != nil {
		t.Fatalf("RunOnce: %v %v", found, err)
	}
	var commit *string
	_ = f.owner.QueryRow(ctx, `SELECT git_commit FROM checkpoints WHERE day = '2026-10-04'`).Scan(&commit)
	var pending int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'checkpoint' AND done_at IS NULL AND run_at = $1`, syd(2026, 10, 5, 0, 10)).Scan(&pending)
	if commit != nil || pending != 1 {
		t.Errorf("unpublished checkpoint commit %v; %d next runs scheduled", commit, pending)
	}
	_ = io.Discard
}
