package issue

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/batch"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/dsse"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/evaluate"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/jobs"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/keys"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/kmssign"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/pack"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/store/storetest"
)

const (
	tenant = "00000000-0000-0000-0000-00000000000a"
	other  = "00000000-0000-0000-0000-00000000000b"
	system = "30000000-0000-0000-0000-00000000000a"
	agentA = "agt_wrenhealth01"
)

var root = filepath.Join("..", "..")
var month = filepath.Join(root, "concierge", "testdata", "partner-month")

type fixture struct {
	owner  *pgx.Conn
	issuer *Issuer
	signer *kmssign.Local
	seq    int64
}

func localSigner(t *testing.T) *kmssign.Local {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	s, err := kmssign.NewLocal(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// newFixture: the partner month's organisation and system (declarations from
// its system.json) and one enrolled agent; no batches yet.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := storetest.MigratedDB(t)
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close(context.Background()) })
	var sys map[string]any
	b, _ := os.ReadFile(filepath.Join(month, "system.json"))
	_ = json.Unmarshal(b, &sys)
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, name) VALUES ($1, $2), ($3, 'Other')`, []any{tenant, sys["organisation"], other}},
		{`INSERT INTO ai_systems (id, tenant_id, name, environment, description, declared_models, declared_regions, timezone, profiles, providers, pack_prefix)
		  VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'WRN')`,
			[]any{system, tenant, sys["ai_system"], sys["environment"], sys["description"], js(sys["declared_models"]), js(sys["declared_regions"]),
				sys["timezone"], js(sys["profiles"]), js(sys["providers"])}},
		{`INSERT INTO agents (id, tenant_id, ai_system_id, public_key) VALUES ($1, $2, $3, $4)`, []any{agentA, tenant, system, []byte(pub)}},
	} {
		if _, err := owner.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	s := localSigner(t)
	f := &fixture{owner: owner, signer: s, issuer: &Issuer{Store: storetest.App(t, db), Signer: s,
		Now: func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) }}}
	f.register(t, s)
	return f
}

// register adds a signing key, valid from well before the fixture's clock.
func (f *fixture) register(t *testing.T, s kmssign.Signer) {
	t.Helper()
	if err := pgx.BeginFunc(context.Background(), f.owner, func(tx pgx.Tx) error {
		return keys.Add(context.Background(), tx, s, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	}); err != nil {
		t.Fatal(err)
	}
}

// storeBatch inserts a batch as ingest would have stored it.
func (f *fixture) storeBatch(t *testing.T, w evaluate.Window, usage []batch.Usage) string {
	t.Helper()
	f.seq++
	if w.Metrics.Tokens == nil {
		w.Metrics = batch.Metrics{Tokens: []batch.TokenCount{}, Evals: []batch.Eval{}, Transitions: []batch.Transition{}, Anomalies: []batch.Anomaly{}}
		w.Observed = batch.Observed{Models: []batch.ObservedModel{}, Versions: []batch.ObservedVersion{}}
		w.Commit = batch.Commit{Root: strings.Repeat("0", 64)}
	}
	if usage == nil {
		usage = []batch.Usage{}
	}
	b := batch.Batch{Schema: batch.Schema, AgentID: agentA, Seq: f.seq, Window: w.Window, SemconvMap: "v2", Metrics: w.Metrics,
		Observed: w.Observed, Usage: usage, Commit: w.Commit}
	payload, _ := json.Marshal(b)
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	start, _ := time.Parse(time.RFC3339, w.Window.Start)
	end, _ := time.Parse(time.RFC3339, w.Window.End)
	if _, err := f.owner.Exec(context.Background(), `INSERT INTO evidence_batches (tenant_id, agent_id, seq, digest, window_start, window_end, payload, envelope)
		VALUES ($1, $2, $3, $4, $5, $6, $7, '{}')`, tenant, agentA, f.seq, digest, start, end, string(payload)); err != nil {
		t.Fatal(err)
	}
	return digest
}

func readCSV(t *testing.T, name string) []map[string]string {
	t.Helper()
	fh, err := os.Open(filepath.Join(month, name))
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	recs, _ := csv.NewReader(fh).ReadAll()
	var out []map[string]string
	for _, r := range recs[1:] {
		m := map[string]string{}
		for i, h := range recs[0] {
			m[h] = r[i]
		}
		out = append(out, m)
	}
	return out
}

func num(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

// loadMonth stores the partner month as production would hold it: each window
// as a batch, each usage row in a batch for its hour (an empty batch where no
// window starts then), and the registers in their tables.
func (f *fixture) loadMonth(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	b, _ := os.ReadFile(filepath.Join(month, "aggregates.json"))
	var agg struct {
		Windows []evaluate.Window `json:"windows"`
	}
	if err := json.Unmarshal(b, &agg); err != nil {
		t.Fatal(err)
	}
	usageAt := map[string][]batch.Usage{}
	for _, u := range readCSV(t, "usage.csv") {
		start, _ := time.Parse(time.RFC3339, u["start"])
		k := start.UTC().Format(time.RFC3339)
		row := batch.Usage{Provider: u["provider"], Model: u["model"], Region: u["region"], InputTokens: num(u["input_tokens"]), OutputTokens: num(u["output_tokens"])}
		if u["requests"] != "" {
			r := num(u["requests"])
			row.Requests = &r
		}
		usageAt[k] = append(usageAt[k], row)
	}
	for _, w := range agg.Windows {
		k := w.Window.Start
		f.storeBatch(t, w, usageAt[k])
		delete(usageAt, k)
	}
	for k, us := range usageAt {
		start, _ := time.Parse(time.RFC3339, k)
		f.storeBatch(t, evaluate.Window{Window: batch.Window{Start: k, End: start.Add(time.Hour).Format(time.RFC3339)}}, us)
	}
	for _, c := range readCSV(t, "changes.csv") {
		f.exec(t, `INSERT INTO register_changes (tenant_id, ai_system_id, change_date, kind, what) VALUES ($1, $2, $3, $4, $5)`, tenant, system, c["date"], c["kind"], c["what"])
	}
	for _, i := range readCSV(t, "incidents.csv") {
		f.exec(t, `INSERT INTO register_incidents (tenant_id, ai_system_id, incident_ref, opened, acknowledged, resolved, summary)
			VALUES ($1, $2, $3, $4, nullif($5, '')::timestamptz, nullif($6, '')::timestamptz, $7)`, tenant, system, i["id"], i["opened"], i["acknowledged"], i["resolved"], i["summary"])
	}
	for _, e := range readCSV(t, "evals.csv") {
		rate, _ := strconv.ParseFloat(e["pass_rate"], 64)
		f.exec(t, `INSERT INTO register_evals (tenant_id, ai_system_id, suite, model, prompt_version, ran_at, pass_rate) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			tenant, system, e["suite"], e["model"], e["prompt_version"], e["ran_at"], rate)
	}
	_ = ctx
}

func (f *fixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.owner.Exec(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

var september = Request{TenantID: tenant, SystemID: system, PeriodStart: "2026-09-01", PeriodEnd: "2026-09-30"}

func predicateOf(t *testing.T, raw []byte) (*dsse.Envelope, pack.Predicate) {
	t.Helper()
	env, err := dsse.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	st, err := pack.DecodeStatement(env.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return env, st.Predicate
}

// The pack's evaluation, from batches and registers in Postgres, equals the
// concierge's evaluation of the same month from its files.
func TestIssueMatchesTheConciergeEvaluation(t *testing.T) {
	f := newFixture(t)
	f.loadMonth(t)
	got, err := f.issuer.Issue(context.Background(), september)
	if err != nil {
		t.Fatal(err)
	}
	env, p := predicateOf(t, got.Envelope)

	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	abs := func(n string) string { a, _ := filepath.Abs(filepath.Join(month, n)); return a }
	args, _ := json.Marshal(map[string]any{"system_path": abs("system.json"), "aggregate_paths": []string{abs("aggregates.json")},
		"usage_paths": []string{abs("usage.csv")}, "changes_path": abs("changes.csv"), "incidents_path": abs("incidents.csv"), "evals_path": abs("evals.csv")})
	cmd := exec.Command(py, "-c", "import sys, json; sys.path.insert(0, 'concierge'); import evaluate; print(json.dumps(evaluate.evaluate(**json.loads(sys.argv[1]))))", string(args))
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("concierge: %v", err)
	}
	var want struct {
		Objectives []pack.Objective `json:"objectives"`
		Coverage   pack.Coverage    `json:"coverage"`
		Lineage    []pack.Lineage   `json:"lineage"`
		Changes    []pack.Change    `json:"changes"`
		Incidents  []pack.Incident  `json:"incidents"`
		LeafCount  int              `json:"leaf_count"`
		Hash       string           `json:"objective_set_hash"`
	}
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]any{
		"objectives": {p.Objectives, want.Objectives}, "coverage": {p.Coverage, want.Coverage}, "lineage": {p.Lineage, want.Lineage},
		"changes": {p.Changes, want.Changes}, "incidents": {p.Incidents, want.Incidents}, "leaf_count": {p.Commitments.LeafCount, want.LeafCount},
		"objective_set_hash": {p.ObjectiveSetHash, want.Hash},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			g, _ := json.MarshalIndent(pair[0], "", " ")
			w, _ := json.MarshalIndent(pair[1], "", " ")
			t.Errorf("%s differs\n pack: %s\n concierge: %s", name, g, w)
		}
	}
	if got.SeqNo != 1 || p.PrevPackDigest != nil || got.PackID != p.PackID || !strings.HasPrefix(p.PackID, "WRN-") || got.Digest != pack.Digest(env) {
		t.Errorf("pack %+v", got)
	}
	if p.IssuedAt != "2026-10-01T19:00:00+10:00" || p.SampleNotice != "" || len(p.BatchDigests) == 0 {
		t.Errorf("issued_at %s, sample notice %q, %d batch digests", p.IssuedAt, p.SampleNotice, len(p.BatchDigests))
	}

	var evals, results, defs int
	ctx := context.Background()
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM evaluations WHERE id = $1`, got.EvaluationID).Scan(&evals)
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM objective_results WHERE evaluation_id = $1`, got.EvaluationID).Scan(&results)
	if err := EnsureObjectiveDefs(ctx, f.issuer.Store); err != nil {
		t.Fatal(err)
	}
	if err := EnsureObjectiveDefs(ctx, f.issuer.Store); err != nil { // once per id and version
		t.Fatal(err)
	}
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM objective_defs`).Scan(&defs)
	if evals != 1 || results != 10 || defs != 10 {
		t.Errorf("stored %d evaluations, %d results, %d objective definitions", evals, results, defs)
	}
}

// Acceptance: 50 concurrent issue requests for one system yield one unbroken chain.
func TestConcurrentIssuesFormOneChain(t *testing.T) {
	f := newFixture(t)
	f.loadMonth(t)
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.issuer.Issue(context.Background(), september); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	rows, _ := f.owner.Query(context.Background(), `SELECT envelope::text FROM packs WHERE ai_system_id = $1 ORDER BY seq_no`, system)
	envs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(envs) != n {
		t.Fatalf("%d packs (%v)", len(envs), err)
	}
	keys := map[string]any{}
	_ = keys
	pub, _ := pack.ParseJWKS(kmssign.JWKS(f.signer.PublicKey(), f.signer.KeyID()))
	var prev *dsse.Envelope
	for i, raw := range envs {
		env, p := predicateOf(t, []byte(raw))
		res, err := pack.Verify(env, pub, prev)
		if err != nil || !res.Verified || p.SeqNo != i+1 {
			t.Fatalf("pack %d: seq %d, %+v, %v", i+1, p.SeqNo, res, err)
		}
		prev = env
	}
}

// The three verifiers (Go CLI, browser verifier under Node, concierge/verify.py) accept a chain of issued packs.
func TestAllThreeVerifiersAcceptIssuedPacks(t *testing.T) {
	f := newFixture(t)
	f.loadMonth(t)
	dir := t.TempDir()
	var files []string
	for i := 1; i <= 3; i++ {
		got, err := f.issuer.Issue(context.Background(), september)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, fmt.Sprintf("pack%d.dsse.json", i))
		if err := os.WriteFile(p, got.Envelope, 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	jwks := filepath.Join(dir, "jwks.json")
	_ = os.WriteFile(jwks, kmssign.JWKS(f.signer.PublicKey(), f.signer.KeyID()), 0o644)
	der, _ := x509.MarshalPKIXPublicKey(f.signer.PublicKey())
	pemPath := filepath.Join(dir, "issuer.pem")
	_ = os.WriteFile(pemPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644)

	run := func(name string, cmd *exec.Cmd, want string) {
		t.Helper()
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), want) {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
	}
	goArgs := append([]string{"run", "./cmd/verify", "--jwks", jwks}, files...)
	run("witnessmark-verify", exec.Command("go", goArgs...), "RESULT: VERIFIED")
	if py, err := exec.LookPath("python3"); err == nil {
		run("verify.py", exec.Command(py, append([]string{"concierge/verify.py", pemPath}, files...)...), "RESULT: VERIFIED")
	}
	if node, err := exec.LookPath("node"); err == nil {
		script := `
import { readFileSync } from "node:fs";
import { verifyEnvelope } from "./web/verify/verify.js";
const [jwks, ...files] = process.argv.slice(1);
const load = (f) => JSON.parse(readFileSync(f, "utf8"));
let prev = null, ok = true;
for (const f of files) {
  const r = await verifyEnvelope(load(f), load(jwks), prev);
  ok = ok && r.verified && (prev === null || r.chain === true);
  prev = load(f);
}
console.log(ok ? "RESULT: VERIFIED" : "RESULT: FAILED");`
		run("verify.js", exec.Command(node, append([]string{"--input-type=module", "-e", script, jwks}, files...)...), "RESULT: VERIFIED")
	}
}

type fakeKMS struct{ key *ecdsa.PrivateKey }

func (f *fakeKMS) GetPublicKey(_ context.Context, in *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	der, _ := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	return &kms.GetPublicKeyOutput{PublicKey: der, KeySpec: types.KeySpecEccNistP256, KeyUsage: types.KeyUsageTypeSignVerify}, nil
}

func (f *fakeKMS) Sign(_ context.Context, in *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	if in.MessageType != types.MessageTypeDigest || len(in.Message) != 32 {
		return nil, errors.New("not a digest")
	}
	sig, err := ecdsa.SignASN1(rand.Reader, f.key, in.Message)
	return &kms.SignOutput{Signature: sig}, err
}

func TestIssueWithKMSRecordsTheKeyARN(t *testing.T) {
	f := newFixture(t)
	f.loadMonth(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	k, err := kmssign.NewKMS(context.Background(), &fakeKMS{key}, "arn:aws:kms:ap-southeast-2:111122223333:key/pack")
	if err != nil {
		t.Fatal(err)
	}
	f.issuer.Signer = k
	f.register(t, k)
	if _, err := f.issuer.Issue(context.Background(), september); err != nil {
		t.Fatal(err)
	}
	var ref string
	_ = f.owner.QueryRow(context.Background(), `SELECT kms_key_id FROM packs`).Scan(&ref)
	if ref != "arn:aws:kms:ap-southeast-2:111122223333:key/pack" {
		t.Errorf("kms_key_id %s", ref)
	}
}

// A signer whose signatures do not verify with its claimed public key (a key
// mix-up, even one registered by mistake) stores nothing.
type wrongKey struct {
	*kmssign.Local
	claimed *ecdsa.PublicKey
}

func (w wrongKey) PublicKey() *ecdsa.PublicKey { return w.claimed }

func TestAKeyMixUpStoresNothing(t *testing.T) {
	f := newFixture(t)
	f.loadMonth(t)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	mixed := wrongKey{Local: localSigner(t), claimed: &k.PublicKey}
	f.register(t, mixed)
	f.issuer.Signer = mixed
	if _, err := f.issuer.Issue(context.Background(), september); err == nil || !strings.Contains(err.Error(), "does not verify") {
		t.Fatalf("err = %v", err)
	}
	var packs, evals int
	_ = f.owner.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM packs), (SELECT count(*) FROM evaluations)`).Scan(&packs, &evals)
	if packs != 0 || evals != 0 {
		t.Errorf("after a failed issue: %d packs, %d evaluations", packs, evals)
	}
}

func TestEmptyPeriodAndOtherTenant(t *testing.T) {
	f := newFixture(t)
	if _, err := f.issuer.Issue(context.Background(), september); !errors.Is(err, ErrNoEvidence) {
		t.Errorf("empty period: %v", err)
	}
	f.loadMonth(t)
	wrong := september
	wrong.TenantID = other
	if _, err := f.issuer.Issue(context.Background(), wrong); err == nil || !strings.Contains(err.Error(), "no AI system") {
		t.Errorf("another tenant's system: %v", err)
	}
}

// Usage repeated across batches (a restart, a second agent) counts once, at
// its largest; the newest incident entry wins; the period root is over the
// window roots in time order (D-027, D-040, D-041).
func TestDedupeSupersedeAndPeriodRoot(t *testing.T) {
	f := newFixture(t)
	h := func(hh int) batch.Window {
		s := time.Date(2026, 9, 5, hh, 0, 0, 0, time.UTC)
		return batch.Window{Start: s.Format(time.RFC3339), End: s.Add(time.Hour).Format(time.RFC3339)}
	}
	w := func(hh int, root string, leaves int64, tokens int64) evaluate.Window {
		return evaluate.Window{Window: h(hh), Commit: batch.Commit{Root: root, Leaves: leaves},
			Metrics: batch.Metrics{Tokens: []batch.TokenCount{{Provider: "openai", Model: "text-embedding-3-small", Region: "unknown", InputTokens: tokens, Calls: 1}},
				Evals: []batch.Eval{}, Transitions: []batch.Transition{}, Anomalies: []batch.Anomaly{}},
			Observed: batch.Observed{Models: []batch.ObservedModel{{Provider: "openai", Model: "text-embedding-3-small"}}, Versions: []batch.ObservedVersion{}}}
	}
	r1, r2, r3 := strings.Repeat("11", 32), strings.Repeat("22", 32), strings.Repeat("33", 32)
	u := func(n int64) []batch.Usage {
		return []batch.Usage{{Provider: "openai", Model: "text-embedding-3-small", InputTokens: n}}
	}
	f.storeBatch(t, w(2, r1, 3, 600), u(900))
	f.storeBatch(t, w(2, r2, 2, 300), u(1000)) // the same hour after a restart: its usage poll saw more
	f.storeBatch(t, w(3, r3, 4, 100), u(100))
	f.exec(t, `INSERT INTO register_incidents (tenant_id, ai_system_id, incident_ref, opened, summary) VALUES ($1, $2, 'INC-1', '2026-09-05T02:30:00Z', 'Spike')`, tenant, system)
	f.exec(t, `INSERT INTO register_incidents (tenant_id, ai_system_id, incident_ref, opened, acknowledged, summary) VALUES ($1, $2, 'INC-1', '2026-09-05T02:30:00Z', '2026-09-05T02:40:00Z', 'Spike')`, tenant, system)
	got, err := f.issuer.Issue(context.Background(), september)
	if err != nil {
		t.Fatal(err)
	}
	_, p := predicateOf(t, got.Envelope)
	cov := p.Coverage.Providers
	if len(cov) != 1 || cov[0].BilledTokens != 1100 || cov[0].TracedTokens != 1000 || cov[0].Region != "" {
		t.Errorf("coverage %+v", cov)
	}
	if len(p.Incidents) != 1 || p.Incidents[0].Acknowledged != "2026-09-05T02:40:00Z" {
		t.Errorf("incidents %+v", p.Incidents)
	}
	// Root over [r1, r2, r3]: node(node(r1, r2), r3).
	node := func(a, b []byte) []byte { s := sha256.Sum256(append(append([]byte{1}, a...), b...)); return s[:] }
	hb := func(s string) []byte { b, _ := hex.DecodeString(s); return b }
	if want := hex.EncodeToString(node(node(hb(r1), hb(r2)), hb(r3))); p.Commitments.TraceMerkleRoot != want || p.Commitments.LeafCount != 9 {
		t.Errorf("root %s leaves %d, want %s 9", p.Commitments.TraceMerkleRoot, p.Commitments.LeafCount, want)
	}
	if len(p.BatchDigests) != 3 {
		t.Errorf("%d batch digests", len(p.BatchDigests))
	}
}

func TestIssuePackJob(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tn := tenant
	enqueue := func(payload any) int64 {
		id, err := jobs.Enqueue(ctx, f.issuer.Store.Pool, jobs.KindIssuePack, &tn, payload, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	w := &jobs.Worker{Store: f.issuer.Store, Handlers: map[string]jobs.Handler{jobs.KindIssuePack: f.issuer.Handler()}}
	state := func(id int64) (attempts int, lastErr *string) {
		_ = f.owner.QueryRow(ctx, `SELECT attempts, last_error FROM jobs WHERE id = $1 AND done_at IS NOT NULL`, id).Scan(&attempts, &lastErr)
		return
	}

	empty := enqueue(JobPayload{AISystemID: system, PeriodStart: "2026-09-01", PeriodEnd: "2026-09-30"})
	bad := enqueue(map[string]string{"ai_system_id": system})
	for range 2 {
		_, _ = w.RunOnce(ctx)
	}
	if a, e := state(empty); a != 1 || e == nil || !strings.Contains(*e, "no committed traces") {
		t.Errorf("empty period: attempts %d, last_error %v", a, e)
	}
	if a, e := state(bad); a != 1 || e == nil {
		t.Errorf("bad payload: attempts %d, last_error %v", a, e)
	}

	f.loadMonth(t)
	good := enqueue(JobPayload{AISystemID: system, PeriodStart: "2026-09-01", PeriodEnd: "2026-09-30"})
	if found, err := w.RunOnce(ctx); !found || err != nil {
		t.Fatalf("RunOnce: %v %v", found, err)
	}
	var packs int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM packs`).Scan(&packs)
	if a, e := state(good); a != 1 || e != nil || packs != 1 {
		t.Errorf("good job: attempts %d, last_error %v, %d packs", a, e, packs)
	}
}

// The issuer signs only with a registered key in date at issued_at (D-042).
func TestIssuerRefusesKeysThatMayNotSign(t *testing.T) {
	f := newFixture(t)
	f.loadMonth(t)
	ctx := context.Background()
	stranger := localSigner(t)
	f.issuer.Signer = stranger
	if _, err := f.issuer.Issue(ctx, september); !errors.Is(err, keys.ErrCannotSign) || !strings.Contains(err.Error(), "not registered") {
		t.Errorf("unregistered key: %v", err)
	}
	f.issuer.Signer = f.signer
	tx := func(fn func(pgx.Tx) error) {
		if err := pgx.BeginFunc(ctx, f.owner, fn); err != nil {
			t.Fatal(err)
		}
	}
	// Retired one second after the fixture's clock: still signs; retired at it: refused.
	tx(func(tx pgx.Tx) error {
		return keys.Retire(ctx, tx, f.signer.KeyID(), time.Date(2026, 10, 1, 9, 0, 1, 0, time.UTC))
	})
	if _, err := f.issuer.Issue(ctx, september); err != nil {
		t.Errorf("a key retired one second later: %v", err)
	}
	f.issuer.Now = func() time.Time { return time.Date(2026, 10, 1, 9, 0, 1, 500_000_000, time.UTC) }
	if _, err := f.issuer.Issue(ctx, september); !errors.Is(err, keys.ErrCannotSign) || !strings.Contains(err.Error(), "retired") {
		t.Errorf("a retired key: %v", err)
	}
	tx(func(tx pgx.Tx) error { return keys.Revoke(ctx, tx, f.signer.KeyID(), "test") })
	f.issuer.Now = func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) }
	if _, err := f.issuer.Issue(ctx, september); !errors.Is(err, keys.ErrCannotSign) || !strings.Contains(err.Error(), "revoked") {
		t.Errorf("a revoked key: %v", err)
	}
}
