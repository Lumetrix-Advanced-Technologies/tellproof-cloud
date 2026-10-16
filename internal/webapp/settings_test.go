package webapp_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

// Acceptance (P6-1): an admin connects Vanta in Settings; the secret is never
// shown again or stored in the clear, a first sync is queued, members who are
// not admins cannot change it, and disconnecting deletes the credentials.
func TestVantaSettings(t *testing.T) {
	rg := newRig(t)
	onboard(t, rg, userA, "A Co", "A system")
	ctx := context.Background()

	page := rg.get(userA, "/settings")
	if page.code != 200 || !strings.Contains(page.body, "Not connected") || !strings.Contains(page.body, "Connect Vanta") {
		t.Fatalf("settings: %d\n%s", page.code, page.body)
	}
	const secret = "a-vanta-client-secret-value"
	form := url.Values{"client_id": {"client-1234abcd"}, "client_secret": {secret}, "resource_id": {"res-1"}, "document_id": {"doc-1"}}
	if r := rg.post(userA, "/settings/vanta", url.Values{"client_id": {"client-1234abcd"}, "resource_id": {"res-1"}}); r.code != 400 || !strings.Contains(r.body, "Not saved") {
		t.Fatalf("connecting without a secret: %d", r.code)
	}
	if r := rg.do(userA, "POST", "/settings/vanta", form, false); r.code != 403 {
		t.Fatalf("a form from another site: %d", r.code)
	}
	r := rg.post(userA, "/settings/vanta", form)
	if r.code != 303 || r.location != "/settings?ok=vanta" {
		t.Fatalf("connect: %d %s", r.code, r.location)
	}
	page = rg.get(userA, "/settings?ok=vanta")
	if !strings.Contains(page.body, "✓</span> Connected") || !strings.Contains(page.body, "abcd") || !strings.Contains(page.body, "A sync is waiting to run") {
		t.Errorf("connected page:\n%s", page.body)
	}
	if strings.Contains(page.body, secret) || strings.Contains(page.body, "client-1234abcd") {
		t.Error("the page shows the credentials")
	}
	var dump string
	_ = rg.owner.QueryRow(ctx, `SELECT row_to_json(i)::text || (SELECT encode(ciphertext, 'escape') FROM integration_secrets) FROM integrations i`).Scan(&dump)
	if strings.Contains(dump, secret) || dump == "" {
		t.Errorf("stored in the clear or not at all: %q", dump)
	}
	var queued int
	_ = rg.owner.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'vanta_sync' AND tenant_id IS NOT NULL`).Scan(&queued)
	if queued != 1 {
		t.Errorf("%d syncs queued, want 1", queued)
	}
	if r := rg.post(userA, "/settings/vanta/sync", url.Values{}); r.code != 303 {
		t.Errorf("sync now: %d", r.code)
	}
	_ = rg.owner.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'vanta_sync' AND tenant_id IS NOT NULL`).Scan(&queued)
	if queued != 1 {
		t.Errorf("sync now with one waiting queued another: %d", queued)
	}
	// Saving again without the secret keeps the stored credentials.
	if r := rg.post(userA, "/settings/vanta", url.Values{"resource_id": {"res-2"}}); r.code != 303 {
		t.Errorf("update without the secret: %d %s", r.code, r.body)
	}

	// A member who is not an admin sees the state but cannot change it.
	if _, err := rg.owner.Exec(ctx, `INSERT INTO members (tenant_id, user_id, email, role) SELECT tenant_id, '20000000-0000-0000-0000-00000000000c', 'c@vendor.example', 'member' FROM members LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	const userC = "20000000-0000-0000-0000-00000000000c"
	if p := rg.get(userC, "/settings"); !strings.Contains(p.body, "Only an admin") || strings.Contains(p.body, `name="client_secret"`) {
		t.Errorf("member's settings page:\n%s", p.body)
	}
	for _, path := range []string{"/settings/vanta", "/settings/vanta/sync", "/settings/vanta/disconnect"} {
		if r := rg.post(userC, path, form); r.code != 403 {
			t.Errorf("member POST %s: %d", path, r.code)
		}
	}

	if r := rg.post(userA, "/settings/vanta/disconnect", url.Values{}); r.code != 303 {
		t.Fatalf("disconnect: %d", r.code)
	}
	var secrets int
	_ = rg.owner.QueryRow(ctx, `SELECT count(*) FROM integration_secrets`).Scan(&secrets)
	if p := rg.get(userA, "/settings"); secrets != 0 || !strings.Contains(p.body, "Not connected") {
		t.Errorf("after disconnect: %d secrets", secrets)
	}

	// Without INTEGRATION_KEY the server says so instead of offering the form.
	rg.app.Secrets = nil
	if p := rg.get(userA, "/settings"); !strings.Contains(p.body, "needs an INTEGRATION_KEY") || strings.Contains(p.body, `name="client_secret"`) {
		t.Errorf("without a key:\n%s", p.body)
	}
}
