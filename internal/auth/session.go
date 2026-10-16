package auth

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store"
)

// Cookie names. All are HttpOnly and SameSite=Lax, and Secure when the app is served over https.
const (
	CookieAccess  = "pl_at"
	CookieRefresh = "pl_rt"
	CookiePKCE    = "pl_pkce"
)

// Member is the signed-in person's membership.
type Member struct {
	ID       string
	TenantID string
	Role     string // admin | member
}

// Identity is who made a request.
type Identity struct {
	Claims *Claims
	Member *Member // nil: signed in, but not yet in an organisation
}

type ctxKey struct{}

// From returns the request's identity (set by Require), or nil.
func From(ctx context.Context) *Identity {
	id, _ := ctx.Value(ctxKey{}).(*Identity)
	return id
}

// WithIdentity puts an identity in a context (tests, handlers).
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// Sessions ties the Supabase session to requests.
type Sessions struct {
	Verifier *Verifier
	Supabase *Supabase
	Store    *store.Store
	BaseURL  string // PUBLIC_BASE_URL, e.g. https://app.plumbline.example
	Now      func() time.Time
}

func (s *Sessions) secure() bool { return strings.HasPrefix(s.BaseURL, "https://") }

func (s *Sessions) setCookie(w http.ResponseWriter, name, value, path string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, MaxAge: int(maxAge.Seconds()),
		HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode})
}

func (s *Sessions) clear(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, MaxAge: -1, HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode})
}

func (s *Sessions) storeTokens(w http.ResponseWriter, t *Tokens) {
	life := time.Duration(t.ExpiresIn) * time.Second
	if life <= 0 {
		life = time.Hour
	}
	s.setCookie(w, CookieAccess, t.AccessToken, "/", life)
	s.setCookie(w, CookieRefresh, t.RefreshToken, "/", 30*24*time.Hour)
}

// Authenticate finds and verifies the request's access token: an
// Authorization bearer token (API clients), else the session cookie, which
// is refreshed through Supabase when it has expired.
func (s *Sessions) Authenticate(w http.ResponseWriter, r *http.Request) (*Claims, error) {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return s.Verifier.Verify(r.Context(), tok)
	}
	c, err := r.Cookie(CookieAccess)
	if err == nil {
		claims, err := s.Verifier.Verify(r.Context(), c.Value)
		if err == nil {
			return claims, nil
		}
		if !errors.Is(err, ErrExpired) {
			return nil, err
		}
	}
	rt, err := r.Cookie(CookieRefresh)
	if err != nil || rt.Value == "" {
		return nil, ErrToken
	}
	t, err := s.Supabase.Refresh(r.Context(), rt.Value)
	if err != nil {
		s.clear(w, CookieAccess, "/")
		s.clear(w, CookieRefresh, "/")
		return nil, ErrToken
	}
	claims, err := s.Verifier.Verify(r.Context(), t.AccessToken)
	if err != nil {
		return nil, err
	}
	s.storeTokens(w, t)
	return claims, nil
}

// MemberOf maps a Supabase user to their membership (member_lookup runs
// before any tenant is known). A person in several organisations gets the
// first by tenant id until the app offers a switcher.
func (s *Sessions) MemberOf(ctx context.Context, userID string) (*Member, error) {
	var m Member
	err := s.Store.Public(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT member_id, tenant_id, role FROM member_lookup($1) ORDER BY tenant_id LIMIT 1`, userID).
			Scan(&m.ID, &m.TenantID, &m.Role)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func wantsHTML(r *http.Request) bool {
	return !strings.HasPrefix(r.URL.Path, "/v1/") && r.Header.Get("Authorization") == ""
}

// Require lets a request through only with a valid session; pages redirect
// to /login, the API answers 401. With requireMember, a signed-in person who
// is not yet in an organisation goes to /onboarding (API: 403).
func (s *Sessions) Require(requireMember bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := s.Authenticate(w, r)
		if err != nil {
			if wantsHTML(r) {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			http.Error(w, `{"error":"sign in required"}`, http.StatusUnauthorized)
			return
		}
		m, err := s.MemberOf(r.Context(), claims.Subject)
		if err != nil {
			http.Error(w, "the session could not be checked", http.StatusServiceUnavailable)
			return
		}
		if m == nil && requireMember {
			if wantsHTML(r) {
				http.Redirect(w, r, "/onboarding", http.StatusSeeOther)
				return
			}
			http.Error(w, `{"error":"not a member of an organisation"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), &Identity{Claims: claims, Member: m})))
	})
}

// SameOrigin refuses state-changing requests from other sites: a cookie-borne
// POST must carry an Origin (or, failing that, a Referer) of the app itself.
// SameSite=Lax cookies already stop most cross-site POSTs; this is the second
// lock. Bearer-token API calls carry no cookies and are not affected.
func (s *Sessions) SameOrigin(next http.Handler) http.Handler {
	base, _ := url.Parse(s.BaseURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || r.Header.Get("Authorization") != "" {
			next.ServeHTTP(w, r)
			return
		}
		src := r.Header.Get("Origin")
		if src == "" {
			src = r.Header.Get("Referer")
		}
		u, err := url.Parse(src)
		if src == "" || err != nil || base == nil || u.Scheme != base.Scheme || u.Host != base.Host {
			http.Error(w, "this form must be sent from the Plumbline app", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Login: POST /auth/login with an email form field. Supabase emails a magic
// link back to /auth/callback; the PKCE verifier waits in a short-lived
// cookie. The answer is the same whether or not the address has an account.
func (s *Sessions) Login(w http.ResponseWriter, r *http.Request) {
	addr, err := mail.ParseAddress(strings.TrimSpace(r.FormValue("email")))
	if err != nil || len(addr.Address) > 254 || addr.Name != "" {
		http.Redirect(w, r, "/login?error=email", http.StatusSeeOther)
		return
	}
	verifier, challenge := NewPKCE()
	if err := s.Supabase.SendMagicLink(r.Context(), addr.Address, s.BaseURL+"/auth/callback", challenge); err != nil && !errors.Is(err, ErrRefused) {
		http.Redirect(w, r, "/login?error=unavailable", http.StatusSeeOther)
		return
	}
	s.setCookie(w, CookiePKCE, verifier, "/auth", 15*time.Minute)
	http.Redirect(w, r, "/login?sent=1", http.StatusSeeOther)
}

// Callback: GET /auth/callback?code=… from the magic link.
func (s *Sessions) Callback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	pk, err := r.Cookie(CookiePKCE)
	if code == "" || err != nil || pk.Value == "" {
		http.Redirect(w, r, "/login?error=link", http.StatusSeeOther)
		return
	}
	t, err := s.Supabase.Exchange(r.Context(), code, pk.Value)
	s.clear(w, CookiePKCE, "/auth")
	if err != nil {
		http.Redirect(w, r, "/login?error=link", http.StatusSeeOther)
		return
	}
	if _, err := s.Verifier.Verify(r.Context(), t.AccessToken); err != nil {
		http.Redirect(w, r, "/login?error=link", http.StatusSeeOther)
		return
	}
	s.storeTokens(w, t)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Logout: POST /auth/logout ends the Supabase session (best effort) and clears the cookies.
func (s *Sessions) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieAccess); err == nil && c.Value != "" {
		_ = s.Supabase.SignOut(r.Context(), c.Value)
	}
	s.clear(w, CookieAccess, "/")
	s.clear(w, CookieRefresh, "/")
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
