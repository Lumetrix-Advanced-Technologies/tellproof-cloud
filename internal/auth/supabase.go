package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Supabase is the part of the Supabase Auth API the app calls: magic links
// with PKCE, the code exchange, refresh and sign-out. Keys and tokens are
// never put in an error or a log line.
type Supabase struct {
	URL     string // https://<ref>.supabase.co
	AnonKey string // the project's publishable (anon) key
	HTTP    *http.Client
}

// Tokens are a Supabase session.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// ErrRefused: Supabase refused the request (bad code, used refresh token...).
var ErrRefused = errors.New("auth: Supabase refused the request")

func (s *Supabase) post(ctx context.Context, path string, query url.Values, body any, bearer string, out any) error {
	b, _ := json.Marshal(body)
	u := s.URL + "/auth/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.AnonKey)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("auth: Supabase %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode >= 500:
		return fmt.Errorf("auth: Supabase %s: HTTP %d", path, resp.StatusCode)
	case resp.StatusCode >= 400:
		return fmt.Errorf("%w (%s: HTTP %d)", ErrRefused, path, resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("auth: Supabase %s: unreadable reply", path)
		}
	}
	return nil
}

// NewPKCE returns a code verifier and its S256 challenge (RFC 7636).
func NewPKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// SendMagicLink emails a sign-in link (creating the user on first sign-in)
// that returns to redirectTo with ?code=.
func (s *Supabase) SendMagicLink(ctx context.Context, email, redirectTo, challenge string) error {
	return s.post(ctx, "/otp", url.Values{"redirect_to": {redirectTo}},
		map[string]any{"email": email, "create_user": true, "code_challenge": challenge, "code_challenge_method": "s256"}, "", nil)
}

// Exchange trades the code from the magic link for a session.
func (s *Supabase) Exchange(ctx context.Context, code, verifier string) (*Tokens, error) {
	var t Tokens
	err := s.post(ctx, "/token", url.Values{"grant_type": {"pkce"}}, map[string]string{"auth_code": code, "code_verifier": verifier}, "", &t)
	if err == nil && (t.AccessToken == "" || t.RefreshToken == "") {
		err = errors.New("auth: Supabase returned no session")
	}
	return &t, err
}

// Refresh renews a session (Supabase rotates the refresh token).
func (s *Supabase) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	var t Tokens
	err := s.post(ctx, "/token", url.Values{"grant_type": {"refresh_token"}}, map[string]string{"refresh_token": refreshToken}, "", &t)
	if err == nil && (t.AccessToken == "" || t.RefreshToken == "") {
		err = errors.New("auth: Supabase returned no session")
	}
	return &t, err
}

// SignOut ends the Supabase session behind an access token.
func (s *Supabase) SignOut(ctx context.Context, accessToken string) error {
	return s.post(ctx, "/logout", nil, map[string]string{}, accessToken, nil)
}
