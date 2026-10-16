package keys_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/dsse"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/keys"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/kmssign"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/pack"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store/storetest"
)

func signer(t *testing.T) *kmssign.Local {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	s, err := kmssign.NewLocal(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func owner(t *testing.T) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), storetest.MigratedDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func inTx(t *testing.T, c *pgx.Conn, fn func(pgx.Tx) error) error {
	t.Helper()
	return pgx.BeginFunc(context.Background(), c, fn)
}

var (
	t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
)

func TestRotationAndRevocationThroughThePublishedJWKS(t *testing.T) {
	c := owner(t)
	ctx := context.Background()
	old, next := signer(t), signer(t)
	mustTx := func(fn func(pgx.Tx) error) {
		if err := inTx(t, c, fn); err != nil {
			t.Fatal(err)
		}
	}
	mustTx(func(tx pgx.Tx) error { return keys.Add(ctx, tx, old, t0) })
	sign := func(s kmssign.Signer, issuedAt string) *dsse.Envelope {
		// The key-status rule needs only a valid statement with an issued_at; use pass vector 1's statement re-signed.
		env, err := dsse.Sign(ctx, s, dsse.PayloadTypeInToto, packPayload(t, issuedAt))
		if err != nil {
			t.Fatal(err)
		}
		return env
	}
	verify := func(env *dsse.Envelope) pack.Result {
		var body []byte
		mustTx(func(tx pgx.Tx) error {
			rows, err := keys.List(ctx, tx)
			if err != nil {
				return err
			}
			body, err = keys.JWKS(rows)
			return err
		})
		ks, err := pack.ParseJWKS(body)
		if err != nil {
			t.Fatalf("the published JWKS does not parse: %v\n%s", err, body)
		}
		r, err := pack.Verify(env, ks, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	beforeRotation := sign(old, "2026-03-01T00:00:00Z")
	if r := verify(beforeRotation); !r.Signature {
		t.Fatalf("a pack from the active key: %v", r.SignatureErr)
	}
	// Rotate: the new key from t1, the old one retired at t1.
	mustTx(func(tx pgx.Tx) error {
		if err := keys.Add(ctx, tx, next, t1); err != nil {
			return err
		}
		return keys.Retire(ctx, tx, old.KeyID(), t1)
	})
	if r := verify(beforeRotation); !r.Signature {
		t.Errorf("after rotation, an old pack: %v", r.SignatureErr)
	}
	if r := verify(sign(old, "2026-06-01T00:00:00Z")); r.Signature || !errors.Is(r.SignatureErr, pack.ErrKeyExpired) {
		t.Errorf("the old key after its retirement: %+v", r)
	}
	if r := verify(sign(next, "2026-06-01T00:00:00Z")); !r.Signature {
		t.Errorf("the new key: %v", r.SignatureErr)
	}
	if r := verify(sign(next, "2026-05-31T23:59:59Z")); r.Signature || !errors.Is(r.SignatureErr, pack.ErrKeyNotYetValid) {
		t.Errorf("the new key before its nbf: %+v", r)
	}
	mustTx(func(tx pgx.Tx) error { return keys.Revoke(ctx, tx, old.KeyID(), "test: compromise") })
	if r := verify(beforeRotation); r.Signature || !errors.Is(r.SignatureErr, pack.ErrKeyRevoked) {
		t.Errorf("after revocation, an old pack: %+v", r)
	}
	if err := inTx(t, c, func(tx pgx.Tx) error { return keys.Retire(ctx, tx, old.KeyID(), t1) }); err == nil {
		t.Error("a key was retired twice")
	}
}

func TestKeysAreNeverRewrittenOrDeleted(t *testing.T) {
	c := owner(t)
	ctx := context.Background()
	s := signer(t)
	if err := inTx(t, c, func(tx pgx.Tx) error { return keys.Add(ctx, tx, s, t0) }); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE signing_keys SET public_jwk = '{}'`,
		`UPDATE signing_keys SET nbf = now()`,
		`UPDATE signing_keys SET key_ref = 'other'`,
		`UPDATE signing_keys SET reason = 'x'`, // only with a revocation
		`DELETE FROM signing_keys`,
		`TRUNCATE signing_keys`,
	} {
		_, err := c.Exec(ctx, q)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "42501" {
			t.Errorf("%s: %v", q, err)
		}
	}
	if _, err := c.Exec(ctx, `UPDATE signing_keys SET exp = $1`, t1); err != nil {
		t.Errorf("retiring: %v", err)
	}
	if _, err := c.Exec(ctx, `UPDATE signing_keys SET exp = $1`, t1.Add(time.Hour)); err == nil {
		t.Error("changing exp after retirement")
	}
}

func TestCheckSigner(t *testing.T) {
	c := owner(t)
	ctx := context.Background()
	s := signer(t)
	check := func(at time.Time) error {
		var err error
		_ = inTx(t, c, func(tx pgx.Tx) error { err = keys.CheckSigner(ctx, tx, s.KeyID(), s.PublicKey(), at); return nil })
		return err
	}
	if err := check(t1); !errors.Is(err, keys.ErrCannotSign) {
		t.Errorf("unregistered: %v", err)
	}
	if err := inTx(t, c, func(tx pgx.Tx) error { return keys.Add(ctx, tx, s, t0.Add(500*time.Millisecond)) }); err != nil {
		t.Fatal(err)
	}
	if err := check(t0); err != nil { // same whole second as nbf: allowed, as the verifiers see floor(nbf)
		t.Errorf("at nbf's second: %v", err)
	}
	if err := check(t0.Add(-time.Second)); !errors.Is(err, keys.ErrCannotSign) {
		t.Errorf("before nbf: %v", err)
	}
	other := signer(t)
	var err error
	_ = inTx(t, c, func(tx pgx.Tx) error { err = keys.CheckSigner(ctx, tx, s.KeyID(), other.PublicKey(), t1); return nil })
	if !errors.Is(err, keys.ErrCannotSign) || !strings.Contains(err.Error(), "different key") {
		t.Errorf("another key under the kid: %v", err)
	}
}
