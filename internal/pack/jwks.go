package pack

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type jwks struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// ParseJWKS reads the issuer's JWKS: ES256 keys (EC P-256) by kid. Unknown
// members are rejected, so a JWKS carrying nbf, exp or a revocation list fails
// here until P4-6 gives them meaning instead of being silently ignored.
func ParseJWKS(data []byte) (map[string]crypto.PublicKey, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var set jwks
	if err := dec.Decode(&set); err != nil {
		return nil, fmt.Errorf("pack: jwks: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("pack: jwks: unexpected data after the JSON document")
	}
	if len(set.Keys) == 0 {
		return nil, errors.New("pack: jwks: no keys")
	}
	keys := make(map[string]crypto.PublicKey, len(set.Keys))
	for i, k := range set.Keys {
		pub, err := k.publicKey()
		if err != nil {
			return nil, fmt.Errorf("pack: jwks: keys[%d] (kid %q): %w", i, k.Kid, err)
		}
		if _, dup := keys[k.Kid]; dup {
			return nil, fmt.Errorf("pack: jwks: duplicate kid %q", k.Kid)
		}
		keys[k.Kid] = pub
	}
	return keys, nil
}

func (k jwk) publicKey() (*ecdsa.PublicKey, error) {
	switch {
	case k.Kid == "":
		return nil, errors.New("kid is required")
	case k.Kty != "EC" || k.Crv != "P-256":
		return nil, fmt.Errorf("want kty EC and crv P-256, got %q %q", k.Kty, k.Crv)
	case k.Alg != "" && k.Alg != "ES256":
		return nil, fmt.Errorf("alg %q is not ES256", k.Alg)
	case k.Use != "" && k.Use != "sig":
		return nil, fmt.Errorf("use %q is not sig", k.Use)
	}
	x, errX := base64.RawURLEncoding.DecodeString(k.X)
	y, errY := base64.RawURLEncoding.DecodeString(k.Y)
	if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
		return nil, errors.New("x and y must be 32-byte unpadded base64url values")
	}
	point := append(append([]byte{0x04}, x...), y...)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		return nil, fmt.Errorf("invalid P-256 point: %w", err)
	}
	return pub, nil
}
