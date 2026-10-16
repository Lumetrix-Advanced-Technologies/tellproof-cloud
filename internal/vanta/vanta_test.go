package vanta

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/lumetrix-advanced-technologies/plumbline/internal/dsse"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/jobs"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/pack"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/secretbox"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/share"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store/storetest"
)

const (
	clientID     = "vanta-client-id-0001"
	clientSecret = "vanta-client-secret-do-not-leak"
	resourceID   = "65f0c0ffee0000000000aaaa"
	documentID   = "doc-plumbline-packs"
)

// fakeVanta is the slice of the Vanta API the sync uses. Like Vanta, it
// keeps one live token per application: minting revokes the previous one.
type fakeVanta struct {
	mu        sync.Mutex
	t         *testing.T
	tokens    int
	live      string
	scopes    []string
	state     map[string][]Resource // full state by resourceId
	puts      int
	uploads   []upload
	failToken int // HTTP status to fail the token request with
	failPut   int
	failAfter int // fail uploads after this many with failUpload
	failUp    int
}

type upload struct {
	document, filename, effective, description string
	content                                    []byte
}

func (f *fakeVanta) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodPost && r.URL.Path == "/oauth/token" {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.failToken != 0 {
			w.WriteHeader(f.failToken)
			_, _ = io.WriteString(w, `{"error":"invalid_client","client_id":"`+body["client_id"]+`"}`)
			return
		}
		if body["client_id"] != clientID || body["client_secret"] != clientSecret || body["grant_type"] != "client_credentials" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.tokens++
		f.live = fmt.Sprintf("tok-%d", f.tokens)
		f.scopes = append(f.scopes, body["scope"])
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": f.live, "expires_in": 3600, "token_type": "Bearer"})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.live || f.live == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPut && r.URL.Path == "/v1/resources/custom_resource":
		if f.failPut != 0 {
			w.WriteHeader(f.failPut)
			return
		}
		var body struct {
			ResourceID string     `json:"resourceId"`
			Resources  []Resource `json:"resources"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Resources == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, x := range body.Resources {
			if !strings.HasPrefix(x.ExternalURL, "https://") || x.DisplayName == "" || x.UniqueID == "" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
		}
		f.state[body.ResourceID] = body.Resources
		f.puts++
		_, _ = io.WriteString(w, `{"success":true}`)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/documents/") && strings.HasSuffix(r.URL.Path, "/uploads"):
		if f.failUp != 0 && len(f.uploads) >= f.failAfter {
			w.WriteHeader(f.failUp)
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(file)
		f.uploads = append(f.uploads, upload{document: strings.Split(r.URL.Path, "/")[3], filename: hdr.Filename, content: b,
			effective: r.FormValue("effectiveAtDate"), description: r.FormValue("description")})
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"f1"}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type fixture struct {
	owner  *pgx.Conn
	st     *store.Store
	box    *secretbox.Box
	fake   *fakeVanta
	sync   *Syncer
	logs   *bytes.Buffer
	clock  time.Time
	member string
}

const (
	tenantA, tenantB = "00000000-0000-0000-0000-00000000000a", "00000000-0000-0000-0000-00000000000b"
	systemA, systemB = "30000000-0000-0000-0000-00000000000a", "30000000-0000-0000-0000-00000000000b"
)

func newBox(t *testing.T) *secretbox.Box {
	t.Helper()
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	b, err := secretbox.FromBase64(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := storetest.MigratedDB(t)
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close(context.Background()) })
	f := &fixture{owner: owner, st: storetest.App(t, db), box: newBox(t), logs: &bytes.Buffer{},
		fake: &fakeVanta{t: t, state: map[string][]Resource{}}, clock: time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)}
	srv := httptest.NewServer(f.fake)
	t.Cleanup(srv.Close)
	f.sync = &Syncer{Store: f.st, Box: f.box, Client: &Client{BaseURL: srv.URL}, AppBase: "https://app.example", VerifyBase: "https://verify.example",
		Now: func() time.Time { return f.clock }, Log: slog.New(slog.NewJSONHandler(f.logs, nil))}
	for _, q := range []string{
		`INSERT INTO tenants (id, name) VALUES ('` + tenantA + `', 'Kestrel'), ('` + tenantB + `', 'Other')`,
		`INSERT INTO members (id, tenant_id, user_id, email) VALUES ('50000000-0000-0000-0000-00000000000a', '` + tenantA + `', gen_random_uuid(), 'a@kestrel.example'),
		   ('50000000-0000-0000-0000-00000000000b', '` + tenantB + `', gen_random_uuid(), 'b@other.example')`,
		`INSERT INTO ai_systems (id, tenant_id, name, environment) VALUES ('` + systemA + `', '` + tenantA + `', 'Claims Triage Agent', 'production'),
		   ('` + systemB + `', '` + tenantB + `', 'Other system', 'production')`,
		`INSERT INTO evaluations (id, tenant_id, ai_system_id, period_start, period_end, objective_set_hash) VALUES
		   ('40000000-0000-0000-0000-00000000000a', '` + tenantA + `', '` + systemA + `', '2026-09-01', '2026-10-01', 'x'),
		   ('40000000-0000-0000-0000-00000000000b', '` + tenantB + `', '` + systemB + `', '2026-09-01', '2026-10-01', 'x')`,
	} {
		if _, err := owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	f.member = "50000000-0000-0000-0000-00000000000a"
	return f
}

// storePack stores the spec's pass vector KST-CTA-000<seq> for a tenant.
func (f *fixture) storePack(t *testing.T, seq int, tenant string, at time.Time) (id, digest string, raw []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "spec", "vectors", "pass", fmt.Sprintf("KST-CTA-%04d.dsse.json", seq)))
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dsse.Decode(raw)
	digest = pack.Digest(env)
	var prev *string
	if seq > 1 {
		p := "sha256:" + strings.Repeat(fmt.Sprint(seq-1), 64)
		prev = &p
	}
	if err := f.owner.QueryRow(context.Background(), `INSERT INTO packs (tenant_id, ai_system_id, evaluation_id, pack_id, seq_no, prev_digest, digest, envelope, kms_key_id, issued_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'k', $9) RETURNING id`,
		tenant, "3"+tenant[1:], "4"+tenant[1:], fmt.Sprintf("KST-CTA-%04d", seq), seq, prev, digest, string(raw), at).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id, digest, raw
}

func (f *fixture) connect(t *testing.T, in Input) {
	t.Helper()
	if err := f.st.WithTenant(context.Background(), tenantA, func(tx pgx.Tx) error {
		return Save(context.Background(), tx, f.box, tenantA, f.member, in)
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) hourly(t *testing.T) {
	t.Helper()
	if err := f.sync.Handler()(context.Background(), jobs.Job{ID: 0, Kind: jobs.KindVantaSync, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) integration(t *testing.T) (cfg Config, lastSync *time.Time, lastError *string) {
	t.Helper()
	var raw []byte
	if err := f.owner.QueryRow(context.Background(), `SELECT config, last_sync_at, last_error FROM integrations WHERE tenant_id = $1`, tenantA).Scan(&raw, &lastSync, &lastError); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &cfg)
	return
}

func full() Input {
	return Input{ClientID: clientID, ClientSecret: clientSecret, ResourceID: resourceID, DocumentID: documentID}
}

// Acceptance (P6-1): the hourly sync puts one resource per objective of the
// latest pack in Vanta as the full state, with a working verify link, and
// uploads each pack once.
func TestHourlySyncSendsFullStateAndUploadsEachPackOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id1, _, raw1 := f.storePack(t, 1, tenantA, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC))
	_, d2, _ := f.storePack(t, 2, tenantA, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	f.connect(t, full())

	f.hourly(t)
	got := f.fake.state[resourceID]
	if len(got) != 10 {
		t.Fatalf("%d resources, want one per objective (10)", len(got))
	}
	r := got[0]
	if r.UniqueID != "plumbline:"+systemA+":EO-LOG-01" || !strings.HasPrefix(r.DisplayName, "Claims Triage Agent (production) · EO-LOG-01 ") {
		t.Errorf("resource = %+v", r)
	}
	p := r.CustomProperties
	if p["packId"] != "KST-CTA-0002" || p["packDigest"] != d2 || p["status"] != "pass" || p["passing"] != true ||
		p["lastEvaluated"] != "2026-09-02T00:00:00Z" || p["periodStart"] == "" {
		t.Errorf("properties = %v", p)
	}
	verify, _ := p["verifyUrl"].(string)
	if !strings.HasPrefix(verify, "https://verify.example/#t=") || r.ExternalURL != verify {
		t.Fatalf("verifyUrl %q, externalUrl %q", verify, r.ExternalURL)
	}
	// The link is a real share link for the system's latest pack, named for Vanta.
	if err := f.st.Public(ctx, func(tx pgx.Tx) error {
		l, err := share.Fetch(ctx, tx, strings.TrimPrefix(verify, "https://verify.example/#t="), nil, "test")
		if err != nil {
			return err
		}
		if l.Status != "ok" || l.PackID != "KST-CTA-0002" || l.VerifierOrg != LinkOrg {
			t.Errorf("the verify link shows %+v", l)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.fake.uploads) != 2 || f.fake.uploads[0].filename != "KST-CTA-0001.dsse.json" || f.fake.uploads[0].document != documentID ||
		f.fake.uploads[0].effective != "2026-08-02T00:00:00Z" || !strings.Contains(f.fake.uploads[0].description, "KST-CTA-0001") {
		t.Fatalf("uploads = %+v", f.fake.uploads)
	}
	// The uploaded file still verifies: same envelope, same digest.
	env, err := dsse.Decode(f.fake.uploads[0].content)
	if err != nil || pack.Digest(env) != func() string { e, _ := dsse.Decode(raw1); return pack.Digest(e) }() {
		t.Fatalf("the uploaded pack differs from the stored one: %v", err)
	}
	if f.fake.scopes[0] != ScopeWriteResource+" "+ScopeWriteDocument {
		t.Errorf("scopes %q", f.fake.scopes[0])
	}
	cfg, last, lastErr := f.integration(t)
	if last == nil || lastErr != nil || cfg.Resources != 10 || cfg.Uploaded != 2 {
		t.Errorf("recorded %+v, %v, %v", cfg, last, lastErr)
	}
	var uploaded int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM vanta_uploads WHERE pack_id = $1`, id1).Scan(&uploaded)
	if uploaded != 1 {
		t.Error("the upload was not recorded")
	}
	var next int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'vanta_sync' AND tenant_id IS NULL AND done_at IS NULL AND run_at > $1`, f.clock).Scan(&next)
	if next != 1 {
		t.Errorf("%d next hourly syncs scheduled, want 1", next)
	}

	// An hour later with a new pack: a fresh token, the same link, the new
	// statuses, and only the new pack uploaded. Tenant B, not connected, has a
	// pack too.
	if _, err := f.owner.Exec(ctx, `INSERT INTO packs (tenant_id, ai_system_id, evaluation_id, pack_id, seq_no, digest, envelope, kms_key_id)
		VALUES ($1, $2, '40000000-0000-0000-0000-00000000000b', 'OTH-0001', 1, $3, $4, 'k')`, tenantB, systemB, "sha256:"+strings.Repeat("b", 64), string(raw1)); err != nil {
		t.Fatal(err)
	}
	f.storePack(t, 3, tenantA, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	f.clock = f.clock.Add(time.Hour)
	f.hourly(t)
	if f.fake.tokens != 2 || f.fake.puts != 2 || len(f.fake.uploads) != 3 || f.fake.uploads[2].filename != "KST-CTA-0003.dsse.json" {
		t.Fatalf("second sync: %d tokens, %d puts, %d uploads", f.fake.tokens, f.fake.puts, len(f.fake.uploads))
	}
	got = f.fake.state[resourceID]
	if len(got) != 10 || got[0].CustomProperties["verifyUrl"] != verify || got[0].CustomProperties["packId"] != "KST-CTA-0003" {
		t.Fatalf("second state: %+v", got[0])
	}
	for _, r := range got {
		if r.CustomProperties["objectiveId"] == "EO-EVAL-01" && (r.CustomProperties["status"] != "attention" || r.CustomProperties["passing"] != false) {
			t.Errorf("EO-EVAL-01 = %v", r.CustomProperties)
		}
	}
	var links int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM share_links WHERE tenant_id = $1`, tenantA).Scan(&links)
	if links != 1 {
		t.Errorf("%d share links, want the one reused", links)
	}
	// Tenant B never connected: nothing of theirs was sent.
	for _, r := range got {
		if strings.Contains(r.UniqueID, systemB) {
			t.Error("another tenant's system was sent")
		}
	}
}

// The credentials and tokens never appear in the database in the clear, in
// the logs, or in a stored error.
func TestCredentialsAreSealedAndNeverLogged(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.storePack(t, 1, tenantA, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	f.connect(t, full())
	f.hourly(t)
	f.fake.failToken = http.StatusUnauthorized
	f.hourly(t)

	var dump string
	if err := f.owner.QueryRow(ctx, `SELECT coalesce(string_agg(t::text, ' '), '') FROM (
		SELECT row_to_json(i)::text AS t FROM integrations i UNION ALL
		SELECT encode(ciphertext, 'escape') || key_id FROM integration_secrets UNION ALL
		SELECT payload::text || coalesce(last_error, '') FROM jobs) x`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	var token string
	_ = f.st.WithTenant(ctx, tenantA, func(tx pgx.Tx) error {
		in, _ := Load(ctx, tx)
		s, err := in.Secrets(ctx, tx, f.box)
		if err != nil {
			t.Fatal(err)
		}
		token = s.Links[systemA].Token
		return nil
	})
	if token == "" {
		t.Fatal("no share token stored")
	}
	for _, secret := range []string{clientSecret, clientID, token, "tok-1"} {
		if strings.Contains(dump, secret) {
			t.Errorf("the database holds %q in the clear", secret)
		}
		if strings.Contains(f.logs.String(), secret) {
			t.Errorf("the logs hold %q", secret)
		}
	}
	_, _, lastErr := f.integration(t)
	if lastErr == nil || *lastErr != Describe(&APIError{Op: "token", Status: 401}) {
		t.Errorf("last_error = %v", lastErr)
	}

	// Another server key cannot open them: the vendor is asked to re-enter them.
	f.sync.Box = newBox(t)
	f.fake.failToken = 0
	f.hourly(t)
	if _, _, lastErr = f.integration(t); lastErr == nil || !strings.Contains(*lastErr, "Enter the client ID and secret again") {
		t.Errorf("last_error with another key = %v", lastErr)
	}
	// A sealed value copied to another tenant's row does not open there.
	var sealed []byte
	_ = f.owner.QueryRow(ctx, `SELECT ciphertext FROM integration_secrets`).Scan(&sealed)
	if _, err := f.box.Open(sealed, binding(tenantB, "x")); !errors.Is(err, secretbox.ErrOpen) {
		t.Error("a sealed value opened under another binding")
	}
}

func TestSettingsValidationAndKeepingCredentials(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	save := func(in Input) error {
		return f.st.WithTenant(ctx, tenantA, func(tx pgx.Tx) error { return Save(ctx, tx, f.box, tenantA, f.member, in) })
	}
	for _, in := range []Input{
		{ClientID: clientID, ResourceID: resourceID}, // no secret on first connect
		{ClientID: clientID, ClientSecret: clientSecret},
		{ClientID: clientID, ClientSecret: clientSecret, ResourceID: "bad id"},
		{ClientID: clientID, ClientSecret: "two words", ResourceID: resourceID},
		{ClientSecret: clientSecret, ResourceID: resourceID},
	} {
		if err := save(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("Save(%+v) = %v, want ErrInvalid", in, err)
		}
	}
	if err := save(full()); err != nil {
		t.Fatal(err)
	}
	// Changing only the document keeps the stored credentials.
	if err := save(Input{ResourceID: resourceID}); err != nil {
		t.Fatal(err)
	}
	f.storePack(t, 1, tenantA, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	f.hourly(t)
	if f.fake.tokens != 1 || len(f.fake.uploads) != 0 || f.fake.scopes[0] != ScopeWriteResource {
		t.Errorf("after clearing the document: %d tokens, %d uploads, scopes %v", f.fake.tokens, len(f.fake.uploads), f.fake.scopes)
	}
	// A new client ID needs its secret.
	if err := save(Input{ClientID: "another-client", ResourceID: resourceID}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a new client ID without a secret: %v", err)
	}
	cfg, _, _ := f.integration(t)
	if cfg.ClientIDHint != "0001" || !cfg.Enabled || cfg.DocumentID != "" {
		t.Errorf("config = %+v", cfg)
	}
}

// A verify link the vendor revoked stays revoked; links are renewed a month
// before they expire.
func TestVerifyLinksRespectRevocationAndRenew(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.storePack(t, 1, tenantA, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	f.connect(t, Input{ClientID: clientID, ClientSecret: clientSecret, ResourceID: resourceID})
	f.hourly(t)
	first := f.fake.state[resourceID][0].CustomProperties["verifyUrl"].(string)

	f.clock = f.clock.Add(340 * 24 * time.Hour)
	f.hourly(t)
	second := f.fake.state[resourceID][0].CustomProperties["verifyUrl"].(string)
	if second == first || second == "" {
		t.Fatal("the link was not renewed a month before it expires")
	}
	var active int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM share_links WHERE revoked_at IS NULL`).Scan(&active)
	if active != 1 {
		t.Errorf("%d active links after renewal, want 1", active)
	}

	if _, err := f.owner.Exec(ctx, `UPDATE share_links SET revoked_at = now() WHERE revoked_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	f.hourly(t)
	f.hourly(t)
	r := f.fake.state[resourceID][0]
	if r.CustomProperties["verifyUrl"] != "" || r.ExternalURL != "https://app.example/systems/"+systemA+"/packs" {
		t.Errorf("after the vendor revoked the link: %+v", r)
	}
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM share_links WHERE revoked_at IS NULL`).Scan(&active)
	if active != 0 {
		t.Error("the sync made a new link after the vendor revoked theirs")
	}
}

func TestErrorsAndRateLimits(t *testing.T) {
	f := newFixture(t)
	for i := 1; i <= 3; i++ {
		f.storePack(t, i, tenantA, time.Date(2026, 7+time.Month(i), 2, 0, 0, 0, 0, time.UTC))
	}
	f.connect(t, full())
	f.fake.failUp, f.fake.failAfter = http.StatusTooManyRequests, 1
	f.hourly(t)
	cfg, _, lastErr := f.integration(t)
	if len(f.fake.uploads) != 1 || cfg.Resources != 10 || cfg.Uploaded != 1 || lastErr == nil || !strings.Contains(*lastErr, "rate limit") {
		t.Fatalf("rate limited: %d uploads, %+v, %v", len(f.fake.uploads), cfg, lastErr)
	}
	f.fake.failUp = 0
	f.hourly(t)
	if cfg, _, lastErr = f.integration(t); len(f.fake.uploads) != 3 || lastErr != nil || cfg.Uploaded != 2 {
		t.Fatalf("the next sync sent the rest: %d uploads, %v", len(f.fake.uploads), lastErr)
	}
	f.fake.failPut = http.StatusUnprocessableEntity
	f.hourly(t)
	if cfg, _, lastErr = f.integration(t); lastErr == nil || !strings.Contains(*lastErr, "did not accept the resources") || cfg.Resources != 10 {
		t.Errorf("rejected resources: %+v, %v", cfg, lastErr)
	}
	f.sync.Client.BaseURL = "http://127.0.0.1:1"
	f.hourly(t)
	if _, _, lastErr = f.integration(t); lastErr == nil || !strings.Contains(*lastErr, "could not be reached") {
		t.Errorf("unreachable: %v", lastErr)
	}
}

func TestSyncNowAndDisconnect(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.storePack(t, 1, tenantA, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	f.connect(t, full())
	// A tenant's own job does not count as the scheduled hourly sync.
	tenant := tenantA
	if err := f.st.WithTenant(ctx, tenantA, func(tx pgx.Tx) error {
		_, err := jobs.Enqueue(ctx, tx, jobs.KindVantaSync, &tenant, map[string]string{"tenant_id": tenant}, time.Time{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := jobs.EnsureScheduled(ctx, f.st, jobs.KindVantaSync, f.clock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var hourly int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'vanta_sync' AND tenant_id IS NULL`).Scan(&hourly)
	if hourly != 1 {
		t.Fatalf("%d hourly jobs, want 1 beside the tenant's", hourly)
	}
	w := &jobs.Worker{Store: f.st, Log: f.sync.Log, Handlers: map[string]jobs.Handler{jobs.KindVantaSync: f.sync.Handler()}}
	if ran, err := w.RunOnce(ctx); !ran || err != nil {
		t.Fatalf("RunOnce = %v, %v", ran, err)
	}
	if f.fake.puts != 1 {
		t.Fatalf("sync now: %d puts", f.fake.puts)
	}

	if err := f.st.WithTenant(ctx, tenantA, func(tx pgx.Tx) error { return Disconnect(ctx, tx, f.box) }); err != nil {
		t.Fatal(err)
	}
	var secrets, active, tenants int
	_ = f.owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM integration_secrets), (SELECT count(*) FROM share_links WHERE revoked_at IS NULL), (SELECT count(*) FROM vanta_tenants())`).Scan(&secrets, &active, &tenants)
	if secrets != 0 || active != 0 || tenants != 0 {
		t.Errorf("after disconnect: %d secrets, %d active links, %d tenants to sync", secrets, active, tenants)
	}
	f.hourly(t)
	if f.fake.puts != 1 {
		t.Error("a disconnected tenant was synced")
	}
	// Reconnecting starts again with a fresh link.
	f.connect(t, full())
	f.hourly(t)
	if f.fake.puts != 2 || f.fake.state[resourceID][0].CustomProperties["verifyUrl"] == "" {
		t.Error("reconnecting did not sync with a new verify link")
	}
}

// Tenants cannot read each other's integration rows.
func TestIntegrationRowsAreTenantScoped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.connect(t, full())
	if err := f.st.WithTenant(ctx, tenantB, func(tx pgx.Tx) error {
		in, err := Load(ctx, tx)
		if in != nil {
			t.Error("tenant B sees tenant A's integration")
		}
		var n int
		_ = tx.QueryRow(ctx, `SELECT count(*) FROM integration_secrets`).Scan(&n)
		if n != 0 {
			t.Error("tenant B sees tenant A's sealed secrets")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
