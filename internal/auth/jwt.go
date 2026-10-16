// Package auth signs people in with Supabase Auth and checks their sessions
// (docs/architecture.md §4.2, D-045). Supabase owns login (magic links) and
// issues the tokens; this package verifies Supabase's ES256 access tokens
// itself against the project's JWKS with crypto/ecdsa (alg, kid, exp, nbf,
// iss, aud, role), maps the subject to a member of one organisation, and keeps
// Supabase's tokens in HttpOnly cookies for the server-rendered app.
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Claims are the verified facts of an access token.
type Claims struct {
	Subject   string // the Supabase user id
	Email     string
	SessionID string
	Expires   time.Time
}

// Errors for a token that does not authenticate anyone.
var (
	ErrToken   = errors.New("auth: the access token is not valid")
	ErrExpired = errors.New("auth: the access token has expired")
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Verifier checks Supabase access tokens.
type Verifier struct {
	JWKSURL  string // https://<ref>.supabase.co/auth/v1/.well-known/jwks.json
	Issuer   string // https://<ref>.supabase.co/auth/v1
	Audience string // default "authenticated"
	HTTP     *http.Client
	Now      func() time.Time
	Leeway   time.Duration // clock skew allowed on exp and nbf; default 30 s

	mu        sync.Mutex
	keys      map[string]*ecdsa.PublicKey
	fetchedAt time.Time
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// key returns the ES256 key for kid, fetching the JWKS when it is stale
// (10 minutes) or does not have kid (at most every 30 seconds, so a flood of
// forged kids cannot hammer Supabase).
func (v *Verifier) key(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	age := v.now().Sub(v.fetchedAt)
	if k, ok := v.keys[kid]; ok && age < 10*time.Minute {
		return k, nil
	}
	if v.keys == nil || age >= 30*time.Second {
		if err := v.fetch(ctx); err != nil {
			if k, ok := v.keys[kid]; ok {
				return k, nil // keep using a known key while Supabase is unreachable
			}
			return nil, err
		}
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: unknown key", ErrToken)
}

func (v *Verifier) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.JWKSURL, nil)
	if err != nil {
		return err
	}
	client := v.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("auth: fetching the Supabase JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: fetching the Supabase JWKS: HTTP %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty, Crv, Kid, Alg, X, Y string
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return fmt.Errorf("auth: the Supabase JWKS does not parse: %w", err)
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || (k.Alg != "" && k.Alg != "ES256") || k.Kid == "" {
			continue // other key types (RSA, or legacy HS256 secrets that are never published) are not used here
		}
		x, errX := base64.RawURLEncoding.DecodeString(k.X)
		y, errY := base64.RawURLEncoding.DecodeString(k.Y)
		if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
			continue
		}
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	v.keys, v.fetchedAt = keys, v.now()
	return nil
}

// audience is a string or an array of strings.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// Verify checks an access token and returns its claims. Only ES256 tokens
// for role "authenticated" are accepted: never "none", never a symmetric
// algorithm, never an anon or service_role token.
func (v *Verifier) Verify(ctx context.Context, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrToken
	}
	hb, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	pb, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || len(sig) != 64 {
		return nil, ErrToken
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if json.Unmarshal(hb, &h) != nil || h.Alg != "ES256" || h.Kid == "" || (h.Typ != "" && h.Typ != "JWT") {
		return nil, ErrToken
	}
	pub, err := v.key(ctx, h.Kid)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, ErrToken
	}
	var c struct {
		Sub       string   `json:"sub"`
		Iss       string   `json:"iss"`
		Aud       audience `json:"aud"`
		Exp       *float64 `json:"exp"`
		Nbf       *float64 `json:"nbf"`
		Role      string   `json:"role"`
		Email     string   `json:"email"`
		SessionID string   `json:"session_id"`
	}
	if err := json.Unmarshal(pb, &c); err != nil {
		return nil, ErrToken
	}
	want := v.Audience
	if want == "" {
		want = "authenticated"
	}
	leeway := v.Leeway
	if leeway == 0 {
		leeway = 30 * time.Second
	}
	now := v.now()
	hasAud := false
	for _, a := range c.Aud {
		hasAud = hasAud || a == want
	}
	switch {
	case c.Iss != v.Issuer || !hasAud || c.Role != "authenticated" || !uuidRE.MatchString(c.Sub) || c.Exp == nil:
		return nil, ErrToken
	case c.Nbf != nil && now.Add(leeway).Before(time.Unix(int64(*c.Nbf), 0)):
		return nil, ErrToken
	case !now.Add(-leeway).Before(time.Unix(int64(*c.Exp), 0)):
		return nil, ErrExpired
	}
	return &Claims{Subject: c.Sub, Email: c.Email, SessionID: c.SessionID, Expires: time.Unix(int64(*c.Exp), 0)}, nil
}
