package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/store/storetest"
)

const user = "20000000-0000-0000-0000-00000000000a"

// fakeSupabase is the Auth API: a JWKS, magic links with PKCE, refresh, logout.
type fakeSupabase struct {
	mu         sync.Mutex
	key        *ecdsa.PrivateKey
	kid        string
	srv        *httptest.Server
	challenge  string
	redirectTo string
	refreshed  int
	loggedOut  int
	refreshOK  bool
}

func newFake(t *testing.T) *fakeSupabase {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f := &fakeSupabase{key: k, kid: "kid-1", refreshOK: true}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSupabase) issuer() string { return f.srv.URL + "/auth/v1" }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// token mints a JWT; edit changes the claims, alg the header's alg.
func (f *fakeSupabase) token(alg string, edit func(map[string]any)) string {
	claims := map[string]any{"sub": user, "iss": f.issuer(), "aud": "authenticated", "role": "authenticated",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "email": "a@example.com", "session_id": "s1"}
	if edit != nil {
		edit(claims)
	}
	h, _ := json.Marshal(map[string]string{"alg": alg, "kid": f.kid, "typ": "JWT"})
	c, _ := json.Marshal(claims)
	in := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(in))
	r, s, _ := ecdsa.Sign(rand.Reader, f.key, sum[:])
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return in + "." + b64(sig)
}

func (f *fakeSupabase) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/auth/v1/.well-known/jwks.json" {
		pub, _ := f.key.PublicKey.Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{
			map[string]string{"kty": "RSA", "kid": "rsa-1", "n": "x", "e": "AQAB"}, // other key types are skipped
			map[string]string{"kty": "EC", "crv": "P-256", "kid": f.kid, "alg": "ES256", "x": b64(pub[1:33]), "y": b64(pub[33:])},
		}})
		return
	}
	if r.Header.Get("apikey") != "anon-key" {
		w.WriteHeader(401)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.URL.Path == "/auth/v1/otp":
		if body["code_challenge_method"] != "s256" || body["create_user"] != true {
			w.WriteHeader(400)
			return
		}
		f.challenge, f.redirectTo = body["code_challenge"].(string), r.URL.Query().Get("redirect_to")
		w.WriteHeader(200)
	case r.URL.Path == "/auth/v1/token" && r.URL.Query().Get("grant_type") == "pkce":
		sum := sha256.Sum256([]byte(body["code_verifier"].(string)))
		if body["auth_code"] != "code-1" || b64(sum[:]) != f.challenge {
			w.WriteHeader(400)
			return
		}
		f.session(w)
	case r.URL.Path == "/auth/v1/token" && r.URL.Query().Get("grant_type") == "refresh_token":
		f.refreshed++
		if !f.refreshOK || body["refresh_token"] != "refresh-1" {
			w.WriteHeader(400)
			return
		}
		f.session(w)
	case r.URL.Path == "/auth/v1/logout":
		f.loggedOut++
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeSupabase) session(w http.ResponseWriter) {
	f.mu.Unlock()
	tok := f.token("ES256", nil)
	f.mu.Lock()
	_ = json.NewEncoder(w).Encode(Tokens{AccessToken: tok, RefreshToken: "refresh-1", ExpiresIn: 3600})
}

func (f *fakeSupabase) verifier() *Verifier {
	return &Verifier{JWKSURL: f.issuer() + "/.well-known/jwks.json", Issuer: f.issuer(), HTTP: f.srv.Client()}
}

func TestVerify(t *testing.T) {
	f := newFake(t)
	v := f.verifier()
	ctx := context.Background()
	c, err := v.Verify(ctx, f.token("ES256", nil))
	if err != nil || c.Subject != user || c.Email != "a@example.com" {
		t.Fatalf("valid token: %+v %v", c, err)
	}
	if _, err := v.Verify(ctx, f.token("ES256", func(m map[string]any) { m["aud"] = []string{"other", "authenticated"} })); err != nil {
		t.Errorf("aud as an array: %v", err)
	}
	hs := strings.Split(f.token("ES256", nil), ".")
	hs[0] = b64([]byte(`{"alg":"HS256","kid":"kid-1"}`))
	none := strings.Split(f.token("ES256", nil), ".")
	none[0] = b64([]byte(`{"alg":"none","kid":"kid-1"}`))
	tampered := strings.Split(f.token("ES256", nil), ".")
	tampered[1] = b64([]byte(`{"sub":"` + user + `","iss":"` + f.issuer() + `","aud":"authenticated","role":"authenticated","exp":9999999999}`))
	for name, tok := range map[string]string{
		"HS256 header":     strings.Join(hs, "."),
		"alg none":         strings.Join(none, "."),
		"tampered claims":  strings.Join(tampered, "."),
		"wrong issuer":     f.token("ES256", func(m map[string]any) { m["iss"] = "https://evil.example/auth/v1" }),
		"wrong audience":   f.token("ES256", func(m map[string]any) { m["aud"] = "anon" }),
		"anon role":        f.token("ES256", func(m map[string]any) { m["role"] = "anon" }),
		"service role":     f.token("ES256", func(m map[string]any) { m["role"] = "service_role" }),
		"no exp":           f.token("ES256", func(m map[string]any) { delete(m, "exp") }),
		"not yet valid":    f.token("ES256", func(m map[string]any) { m["nbf"] = time.Now().Add(time.Hour).Unix() }),
		"subject not uuid": f.token("ES256", func(m map[string]any) { m["sub"] = "admin" }),
		"malformed":        "a.b",
		"empty":            "",
	} {
		if _, err := v.Verify(ctx, tok); !errors.Is(err, ErrToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := v.Verify(ctx, f.token("ES256", func(m map[string]any) { m["exp"] = time.Now().Add(-time.Minute).Unix() })); !errors.Is(err, ErrExpired) {
		t.Errorf("expired: %v", err)
	}
	if _, err := v.Verify(ctx, f.token("ES256", func(m map[string]any) { m["exp"] = time.Now().Add(-10 * time.Second).Unix() })); err != nil {
		t.Errorf("expired within the leeway: %v", err)
	}
	// Supabase rotates its key: a token under the new kid makes the verifier refetch (after the 30 s guard).
	nk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f.mu.Lock()
	f.key, f.kid = nk, "kid-2"
	f.mu.Unlock()
	if _, err := v.Verify(ctx, f.token("ES256", nil)); err == nil {
		t.Error("refetched within 30 s of the last fetch")
	}
	v.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if _, err := v.Verify(ctx, f.token("ES256", nil)); err != nil {
		t.Errorf("a rotated key after the guard: %v", err)
	}
}

type rig struct {
	f  *fakeSupabase
	s  *Sessions
	db string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	f := newFake(t)
	db := storetest.MigratedDB(t)
	s := &Sessions{Verifier: f.verifier(), Supabase: &Supabase{URL: f.srv.URL, AnonKey: "anon-key", HTTP: f.srv.Client()},
		Store: storetest.App(t, db), BaseURL: "https://app.example"}
	return &rig{f: f, s: s, db: db}
}

func (rg *rig) addMember(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, rg.db)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(ctx)
	for _, q := range []string{
		`INSERT INTO tenants (id, name) VALUES ('00000000-0000-0000-0000-00000000000a', 'A')`,
		`INSERT INTO members (id, tenant_id, user_id, email) VALUES ('10000000-0000-0000-0000-00000000000a', '00000000-0000-0000-0000-00000000000a', '` + user + `', 'a@example.com')`,
	} {
		if _, err := owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}

func cookies(rec *httptest.ResponseRecorder) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		out[c.Name] = c
	}
	return out
}

func (rg *rig) call(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRequire(t *testing.T) {
	rg := newRig(t)
	var seen *Identity
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = From(r.Context()); w.WriteHeader(200) })
	member, anyone := rg.s.Require(true, ok), rg.s.Require(false, ok)

	// Not signed in: pages redirect to /login, the API answers 401.
	if rec := rg.call(member, httptest.NewRequest("GET", "/packs", nil)); rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=%2Fpacks") {
		t.Errorf("page, signed out: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := rg.call(member, httptest.NewRequest("GET", "/v1/systems", nil)); rec.Code != 401 {
		t.Errorf("API, signed out: %d", rec.Code)
	}
	// Signed in, no organisation yet: onboarding (API 403); allowed where membership is not required.
	req := httptest.NewRequest("GET", "/packs", nil)
	req.AddCookie(&http.Cookie{Name: CookieAccess, Value: rg.f.token("ES256", nil)})
	if rec := rg.call(member, req); rec.Code != 303 || rec.Header().Get("Location") != "/onboarding" {
		t.Errorf("no member: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := rg.call(anyone, req); rec.Code != 200 || seen.Member != nil || seen.Claims.Subject != user {
		t.Errorf("signed in without an organisation: %d %+v", rec.Code, seen)
	}
	rg.addMember(t)
	if rec := rg.call(member, req); rec.Code != 200 || seen.Member == nil || seen.Member.TenantID != "00000000-0000-0000-0000-00000000000a" || seen.Member.Role != "admin" {
		t.Errorf("member: %d %+v", rec.Code, seen)
	}
	api := httptest.NewRequest("GET", "/v1/systems", nil)
	api.Header.Set("Authorization", "Bearer "+rg.f.token("ES256", nil))
	if rec := rg.call(member, api); rec.Code != 200 {
		t.Errorf("bearer: %d", rec.Code)
	}

	// An expired access cookie is refreshed through Supabase and replaced.
	exp := httptest.NewRequest("GET", "/packs", nil)
	exp.AddCookie(&http.Cookie{Name: CookieAccess, Value: rg.f.token("ES256", func(m map[string]any) { m["exp"] = time.Now().Add(-time.Hour).Unix() })})
	exp.AddCookie(&http.Cookie{Name: CookieRefresh, Value: "refresh-1"})
	rec := rg.call(member, exp)
	if c := cookies(rec); rec.Code != 200 || c[CookieAccess] == nil || !c[CookieAccess].HttpOnly || !c[CookieAccess].Secure || c[CookieAccess].SameSite != http.SameSiteLaxMode || rg.f.refreshed != 1 {
		t.Errorf("refresh: %d %v refreshed %d", rec.Code, c, rg.f.refreshed)
	}
	// A refused refresh signs the person out.
	rg.f.refreshOK = false
	rec = rg.call(member, exp)
	if c := cookies(rec); rec.Code != 303 || c[CookieAccess] == nil || c[CookieAccess].MaxAge >= 0 {
		t.Errorf("refused refresh: %d %v", rec.Code, c)
	}
	// A forged cookie is not refreshed into a session.
	bad := httptest.NewRequest("GET", "/packs", nil)
	bad.AddCookie(&http.Cookie{Name: CookieAccess, Value: rg.f.token("ES256", func(m map[string]any) { m["iss"] = "x" })})
	if rec := rg.call(member, bad); rec.Code != 303 {
		t.Errorf("forged cookie: %d", rec.Code)
	}
}

func TestSameOrigin(t *testing.T) {
	rg := newRig(t)
	h := rg.s.SameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	post := func(headers map[string]string) int {
		req := httptest.NewRequest("POST", "/packs", strings.NewReader(""))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return rg.call(h, req).Code
	}
	for name, c := range map[string]struct {
		h    map[string]string
		want int
	}{
		"same origin":          {map[string]string{"Origin": "https://app.example"}, 200},
		"same-origin referer":  {map[string]string{"Referer": "https://app.example/packs"}, 200},
		"another site":         {map[string]string{"Origin": "https://evil.example"}, 403},
		"http downgrade":       {map[string]string{"Origin": "http://app.example"}, 403},
		"no origin or referer": {map[string]string{}, 403},
		"bearer API call":      {map[string]string{"Authorization": "Bearer x"}, 200},
	} {
		if got := post(c.h); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	if rec := rg.call(h, httptest.NewRequest("GET", "/packs", nil)); rec.Code != 200 {
		t.Errorf("GET: %d", rec.Code)
	}
}

func TestMagicLinkSignIn(t *testing.T) {
	rg := newRig(t)
	login := httptest.NewRequest("POST", "/auth/login", strings.NewReader(url.Values{"email": {"a@example.com"}}.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := rg.call(http.HandlerFunc(rg.s.Login), login)
	pk := cookies(rec)[CookiePKCE]
	if rec.Code != 303 || rec.Header().Get("Location") != "/login?sent=1" || pk == nil || pk.Path != "/auth" || !pk.HttpOnly {
		t.Fatalf("login: %d %s %+v", rec.Code, rec.Header().Get("Location"), pk)
	}
	if rg.f.redirectTo != "https://app.example/auth/callback" || rg.f.challenge == "" || rg.f.challenge == pk.Value {
		t.Errorf("otp request: redirect %q, challenge %q", rg.f.redirectTo, rg.f.challenge)
	}
	for _, bad := range []string{"", "not-an-email", "Name <a@example.com>"} {
		r := httptest.NewRequest("POST", "/auth/login", strings.NewReader(url.Values{"email": {bad}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if rec := rg.call(http.HandlerFunc(rg.s.Login), r); rec.Header().Get("Location") != "/login?error=email" {
			t.Errorf("email %q: %s", bad, rec.Header().Get("Location"))
		}
	}

	cb := httptest.NewRequest("GET", "/auth/callback?code=code-1", nil)
	cb.AddCookie(pk)
	rec = rg.call(http.HandlerFunc(rg.s.Callback), cb)
	c := cookies(rec)
	if rec.Code != 303 || rec.Header().Get("Location") != "/" || c[CookieAccess] == nil || c[CookieRefresh] == nil || c[CookiePKCE].MaxAge >= 0 {
		t.Fatalf("callback: %d %s %v", rec.Code, rec.Header().Get("Location"), c)
	}
	if _, err := rg.s.Verifier.Verify(context.Background(), c[CookieAccess].Value); err != nil {
		t.Errorf("the stored session: %v", err)
	}
	// Without the verifier cookie, or with another verifier, the code is useless.
	if rec := rg.call(http.HandlerFunc(rg.s.Callback), httptest.NewRequest("GET", "/auth/callback?code=code-1", nil)); rec.Header().Get("Location") != "/login?error=link" {
		t.Errorf("callback without PKCE cookie: %s", rec.Header().Get("Location"))
	}
	stolen := httptest.NewRequest("GET", "/auth/callback?code=code-1", nil)
	stolen.AddCookie(&http.Cookie{Name: CookiePKCE, Value: "someone-elses-verifier"})
	if rec := rg.call(http.HandlerFunc(rg.s.Callback), stolen); rec.Header().Get("Location") != "/login?error=link" {
		t.Errorf("callback with the wrong verifier: %s", rec.Header().Get("Location"))
	}

	out := httptest.NewRequest("POST", "/auth/logout", nil)
	out.AddCookie(c[CookieAccess])
	rec = rg.call(http.HandlerFunc(rg.s.Logout), out)
	if c := cookies(rec); rec.Header().Get("Location") != "/login" || c[CookieAccess].MaxAge >= 0 || c[CookieRefresh].MaxAge >= 0 || rg.f.loggedOut != 1 {
		t.Errorf("logout: %v %d", c, rg.f.loggedOut)
	}
}
