package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/dsse"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/pack"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/share"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/store"
)

const memberA = "10000000-0000-0000-0000-00000000000a"

type shareFixture struct {
	*fixture
	app   *store.Store
	packs []string // packs.id for vectors 1..3
	files []string
}

// shareFixture stores the three chained pass vectors as tenant A's packs.
func newShareFixture(t *testing.T) *shareFixture {
	t.Helper()
	f := newFixture(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO members (id, tenant_id, user_id, email) VALUES ('` + memberA + `', '` + tenantA + `', '20000000-0000-0000-0000-00000000000a', 'a@example.com')`,
		`INSERT INTO evaluations (id, tenant_id, ai_system_id, period_start, period_end, objective_set_hash)
		   VALUES ('40000000-0000-0000-0000-00000000000a', '` + tenantA + `', '` + systemA + `', '2026-09-01', '2026-10-01', 'x')`,
	} {
		if _, err := f.owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	sf := &shareFixture{fixture: f, app: f.store}
	var prev *string
	for i := 1; i <= 3; i++ {
		file := filepath.Join("..", "..", "spec", "vectors", "pass", "KST-CTA-000"+string(rune('0'+i))+".dsse.json")
		raw, _ := os.ReadFile(file)
		env, _ := dsse.Decode(raw)
		d := pack.Digest(env)
		var id string
		if err := f.owner.QueryRow(ctx, `INSERT INTO packs (tenant_id, ai_system_id, evaluation_id, pack_id, seq_no, prev_digest, digest, envelope, kms_key_id)
			VALUES ($1, $2, '40000000-0000-0000-0000-00000000000a', $3, $4, $5, $6, $7, 'k') RETURNING id`,
			tenantA, systemA, "KST-CTA-000"+string(rune('0'+i)), i, prev, d, string(raw)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		prev = &d
		sf.packs = append(sf.packs, id)
		sf.files = append(sf.files, file)
	}
	return sf
}

func (f *shareFixture) create(t *testing.T, target share.Target, org string, expires time.Time) (*share.Link, error) {
	t.Helper()
	var l *share.Link
	err := f.app.WithTenant(context.Background(), tenantA, func(tx pgx.Tx) error {
		var err error
		l, err = share.Create(context.Background(), tx, memberA, target, org, expires, time.Now())
		return err
	})
	return l, err
}

func (f *shareFixture) get(t *testing.T, token string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/v1/verify/"+token, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/141.0 Safari/537.36")
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("headers %v", resp.Header)
	}
	return resp.StatusCode, b
}

// Acceptance (P5-1): tokens hashed; 410 on revoke or expiry; verifier_views rows.
func TestShareLinksAndTheVerifyAPI(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	in30 := time.Now().Add(30 * 24 * time.Hour)
	latest, err := f.create(t, share.Target{SystemID: systemA}, "Bank X", in30)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := f.create(t, share.Target{PackID: f.packs[0]}, "Insurer Y", in30)
	if err != nil {
		t.Fatal(err)
	}

	// The latest pack, with the issuer's stored digest of the one before it.
	code, body := f.get(t, latest.Token)
	if code != 200 {
		t.Fatalf("HTTP %d %s", code, body)
	}
	var got struct {
		Envelope json.RawMessage `json:"envelope"`
		Prev     *string         `json:"prev_envelope_digest"`
		Meta     map[string]any  `json:"pack_meta"`
	}
	_ = json.Unmarshal(body, &got)
	want3, _ := os.ReadFile(f.files[2])
	env2raw, _ := os.ReadFile(f.files[1])
	env2, _ := dsse.Decode(env2raw)
	var a, b any
	_ = json.Unmarshal(got.Envelope, &a)
	_ = json.Unmarshal(want3, &b)
	if !jsonEqual(a, b) || got.Prev == nil || *got.Prev != pack.Digest(env2) || got.Meta["seq_no"] != float64(3) || got.Meta["verifier_org"] != "Bank X" {
		t.Errorf("response %s", body)
	}
	// The browser verifier accepts exactly this response.
	if node, err := exec.LookPath("node"); err == nil {
		resp := filepath.Join(t.TempDir(), "resp.json")
		_ = os.WriteFile(resp, body, 0o644)
		script := `
import { readFileSync } from "node:fs";
import { verifyEnvelope } from "./web/verify/verify.js";
const body = JSON.parse(readFileSync(process.argv[1], "utf8"));
const jwks = JSON.parse(readFileSync("spec/vectors/test-jwks.json", "utf8"));
const r = await verifyEnvelope(body.envelope, jwks, body.prev_envelope_digest);
console.log(r.verified && r.chain === true ? "VERIFIED" : "FAILED " + JSON.stringify(r));`
		cmd := exec.Command(node, "--input-type=module", "-e", script, resp)
		cmd.Dir = filepath.Join("..", "..")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.HasPrefix(string(out), "VERIFIED") {
			t.Errorf("verify.js on the API response: %v %s", err, out)
		}
	}
	// A pinned pack 1: no previous pack.
	if code, body := f.get(t, pinned.Token); code != 200 || !bytes.Contains(body, []byte(`"prev_envelope_digest":null`)) {
		t.Errorf("pinned pack 1: %d %s", code, body)
	}

	// Views are logged with a hashed address and a browser family, never the raw values.
	var views, hashed int
	var family string
	_ = f.owner.QueryRow(ctx, `SELECT count(*), count(ip_hash), min(user_agent_family) FROM verifier_views`).Scan(&views, &hashed, &family)
	if views != 2 || hashed != 2 || family != "chrome" {
		t.Errorf("views %d, hashed %d, family %q", views, hashed, family)
	}
	var leaked int
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM share_links WHERE token_hash = $1 OR encode(token_hash, 'escape') LIKE '%' || $2 || '%'`,
		[]byte(latest.Token), latest.Token).Scan(&leaked)
	if leaked != 0 {
		t.Error("a token was stored in the clear")
	}
	var summaries []share.Summary
	_ = f.app.WithTenant(ctx, tenantA, func(tx pgx.Tx) error {
		summaries, err = share.List(ctx, tx)
		return err
	})
	if len(summaries) != 2 || summaries[0].Views+summaries[1].Views != 2 || summaries[0].Viewers != 1 {
		t.Errorf("summaries %+v", summaries)
	}

	// Revoked and expired links answer 410; unknown and malformed ones 404.
	_ = f.app.WithTenant(ctx, tenantA, func(tx pgx.Tx) error { return share.Revoke(ctx, tx, latest.ID) })
	if code, _ := f.get(t, latest.Token); code != http.StatusGone {
		t.Errorf("revoked: %d", code)
	}
	if _, err := f.owner.Exec(ctx, `UPDATE share_links SET expires_at = now() - interval '1 second' WHERE id = $1`, pinned.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.get(t, pinned.Token); code != http.StatusGone {
		t.Errorf("expired: %d", code)
	}
	if code, _ := f.get(t, "AAAAAAAAAAAAAAAAAAAAAA"); code != http.StatusNotFound {
		t.Errorf("unknown: %d", code)
	}
	if code, _ := f.get(t, "short"); code != http.StatusNotFound {
		t.Errorf("malformed: %d", code)
	}
	_ = f.owner.QueryRow(ctx, `SELECT count(*) FROM verifier_views`).Scan(&views)
	if views != 2 {
		t.Errorf("refused fetches logged views: %d", views)
	}
}

func TestShareCreateRules(t *testing.T) {
	f := newShareFixture(t)
	now := time.Now()
	for name, c := range map[string]struct {
		target  share.Target
		org     string
		expires time.Time
	}{
		"no target":       {share.Target{}, "Bank", now.Add(time.Hour)},
		"two targets":     {share.Target{PackID: f.packs[0], SystemID: systemA}, "Bank", now.Add(time.Hour)},
		"no organisation": {share.Target{PackID: f.packs[0]}, "  ", now.Add(time.Hour)},
		"already expired": {share.Target{PackID: f.packs[0]}, "Bank", now.Add(-time.Second)},
		"over a year":     {share.Target{PackID: f.packs[0]}, "Bank", now.Add(400 * 24 * time.Hour)},
		"unknown pack":    {share.Target{PackID: "50000000-0000-0000-0000-000000000000"}, "Bank", now.Add(time.Hour)},
	} {
		if _, err := f.create(t, c.target, c.org, c.expires); !errors.Is(err, share.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Another tenant cannot share tenant A's pack: row-level security hides it.
	err := f.app.WithTenant(context.Background(), tenantB, func(tx pgx.Tx) error {
		_, err := share.Create(context.Background(), tx, memberA, share.Target{PackID: f.packs[0]}, "Bank", now.Add(time.Hour), now)
		return err
	})
	if !errors.Is(err, share.ErrInvalid) {
		t.Errorf("tenant B sharing A's pack: %v", err)
	}
	// A system link before any pack: 404 until the first pack.
	if _, err := f.owner.Exec(context.Background(), `INSERT INTO ai_systems (id, tenant_id, name) VALUES ('30000000-0000-0000-0000-0000000000a2', $1, 'New')`, tenantA); err != nil {
		t.Fatal(err)
	}
	l, err := f.create(t, share.Target{SystemID: "30000000-0000-0000-0000-0000000000a2"}, "Bank", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := f.get(t, l.Token); code != http.StatusNotFound {
		t.Errorf("no pack yet: %d", code)
	}
}

func TestUAFamily(t *testing.T) {
	for ua, want := range map[string]string{
		"": "unknown", "curl/8.5.0": "curl", "tellproof-verify/v0.1": "tellproof-verify",
		"Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Chrome/141.0 Safari/537.36 Edg/141.0": "edge",
		"Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Version/18.0 Safari/605.1.15":             "safari",
		"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0":                "firefox",
		"python-requests/2.32": "other",
	} {
		if got := share.UAFamily(ua); got != want {
			t.Errorf("UAFamily(%q) = %s, want %s", ua, got, want)
		}
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
