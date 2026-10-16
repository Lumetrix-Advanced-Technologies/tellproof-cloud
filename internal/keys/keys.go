// Package keys manages the issuer's signing keys (D-042): the signing_keys
// table, the JWKS published at /.well-known/jwks.json, and the check that a
// key may sign a pack issued now. Keys are added, retired and revoked by ops
// with the owner role; the app role only reads them.
package keys

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/kmssign"
)

// Row is one signing key.
type Row struct {
	KID       string
	JWK       map[string]string
	KeyRef    string
	NotBefore time.Time
	Expires   *time.Time
	RevokedAt *time.Time
	Reason    string
}

// Add registers a signer's key, valid from nbf. Adding the same key again is an error.
func Add(ctx context.Context, q pgx.Tx, s kmssign.Signer, nbf time.Time) error {
	jwk := kmssign.JWK(s.PublicKey(), s.KeyID())
	pub := map[string]string{"kty": jwk["kty"], "crv": jwk["crv"], "x": jwk["x"], "y": jwk["y"]}
	b, _ := json.Marshal(pub)
	_, err := q.Exec(ctx, `INSERT INTO signing_keys (kid, public_jwk, key_ref, nbf) VALUES ($1, $2, $3, $4)`, s.KeyID(), string(b), s.KeyRef(), nbf)
	return err
}

// Retire stops a key signing new packs from at; packs issued before stay valid.
func Retire(ctx context.Context, q pgx.Tx, kid string, at time.Time) error {
	tag, err := q.Exec(ctx, `UPDATE signing_keys SET exp = $2 WHERE kid = $1 AND exp IS NULL`, kid, at)
	if err == nil && tag.RowsAffected() != 1 {
		return fmt.Errorf("keys: no unretired key %s", kid)
	}
	return err
}

// Revoke withdraws a key entirely: every pack it signed stops verifying.
func Revoke(ctx context.Context, q pgx.Tx, kid, reason string) error {
	tag, err := q.Exec(ctx, `UPDATE signing_keys SET revoked_at = now(), reason = $2 WHERE kid = $1 AND revoked_at IS NULL`, kid, reason)
	if err == nil && tag.RowsAffected() != 1 {
		return fmt.Errorf("keys: no unrevoked key %s", kid)
	}
	return err
}

// List returns every key, oldest first.
func List(ctx context.Context, q pgx.Tx) ([]Row, error) {
	rows, err := q.Query(ctx, `SELECT kid, public_jwk, key_ref, nbf, exp, revoked_at, coalesce(reason, '') FROM signing_keys ORDER BY nbf, kid`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Row, error) {
		var k Row
		var raw []byte
		if err := r.Scan(&k.KID, &raw, &k.KeyRef, &k.NotBefore, &k.Expires, &k.RevokedAt, &k.Reason); err != nil {
			return k, err
		}
		return k, json.Unmarshal(raw, &k.JWK)
	})
}

// JWKS renders the key set as every verifier reads it (D-042): all keys (a
// revoked key stays listed so verifiers can say why), nbf and exp as whole
// seconds, and the revoked list.
func JWKS(rows []Row) ([]byte, error) {
	type jwk struct {
		Kty string `json:"kty"`
		Crv string `json:"crv"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		X   string `json:"x"`
		Y   string `json:"y"`
		Nbf int64  `json:"nbf"`
		Exp *int64 `json:"exp,omitempty"`
	}
	type revoked struct {
		Kid       string `json:"kid"`
		RevokedAt int64  `json:"revoked_at"`
		Reason    string `json:"reason,omitempty"`
	}
	doc := struct {
		Keys    []jwk     `json:"keys"`
		Revoked []revoked `json:"revoked,omitempty"`
	}{Keys: []jwk{}}
	for _, r := range rows {
		k := jwk{Kty: r.JWK["kty"], Crv: r.JWK["crv"], Kid: r.KID, Use: "sig", Alg: "ES256", X: r.JWK["x"], Y: r.JWK["y"], Nbf: r.NotBefore.Unix()}
		if r.Expires != nil {
			e := r.Expires.Unix()
			k.Exp = &e
		}
		doc.Keys = append(doc.Keys, k)
		if r.RevokedAt != nil {
			doc.Revoked = append(doc.Revoked, revoked{Kid: r.KID, RevokedAt: r.RevokedAt.Unix(), Reason: r.Reason})
		}
	}
	if len(doc.Keys) == 0 {
		return nil, errors.New("keys: no signing keys registered")
	}
	return json.Marshal(doc)
}

// ErrCannotSign: the signer's key is not registered, not yet valid, retired or revoked.
var ErrCannotSign = errors.New("keys: the signing key may not sign packs now")

// CheckSigner reports whether pub (kid) may sign a pack issued at issuedAt,
// by the same whole-second rule the verifiers apply to the published JWKS.
func CheckSigner(ctx context.Context, q pgx.Tx, kid string, pub *ecdsa.PublicKey, issuedAt time.Time) error {
	var raw []byte
	var nbf time.Time
	var exp, revoked *time.Time
	err := q.QueryRow(ctx, `SELECT public_jwk, nbf, exp, revoked_at FROM signing_keys WHERE kid = $1`, kid).Scan(&raw, &nbf, &exp, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s is not registered (plumbline-server keys add)", ErrCannotSign, kid)
	}
	if err != nil {
		return err
	}
	var jwk map[string]string
	if err := json.Unmarshal(raw, &jwk); err != nil {
		return err
	}
	want := kmssign.JWK(pub, kid)
	sec := issuedAt.Unix()
	switch {
	case jwk["x"] != want["x"] || jwk["y"] != want["y"]:
		return fmt.Errorf("%w: the registered key %s is a different key", ErrCannotSign, kid)
	case revoked != nil:
		return fmt.Errorf("%w: %s is revoked", ErrCannotSign, kid)
	case sec < nbf.Unix():
		return fmt.Errorf("%w: %s is not valid until %s", ErrCannotSign, kid, nbf.UTC().Format(time.RFC3339))
	case exp != nil && sec >= exp.Unix():
		return fmt.Errorf("%w: %s was retired at %s", ErrCannotSign, kid, exp.UTC().Format(time.RFC3339))
	}
	return nil
}
