// Package kmssign signs packs: with an AWS KMS asymmetric key in staging and
// production, or a local P-256 key in development and tests. KMS signs the
// 32-byte SHA-256 of the DSSE PAE with MessageType DIGEST, because it accepts
// at most 4,096 bytes of message (D-006). Both return ASN.1 DER signatures.
package kmssign

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/dsse"
)

// Signer is a pack signer: a dsse.Signer whose public key and key IDs are known.
type Signer interface {
	dsse.Signer
	PublicKey() *ecdsa.PublicKey
	KeyID() string  // the JWKS kid packs are signed under
	KeyRef() string // what packs.kms_key_id records: the KMS key ARN, or "local:<kid>"
}

// Client is the part of the KMS API the signer uses.
type Client interface {
	Sign(ctx context.Context, in *kms.SignInput, opts ...func(*kms.Options)) (*kms.SignOutput, error)
	GetPublicKey(ctx context.Context, in *kms.GetPublicKeyInput, opts ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
}

// KMS signs with an ECC_NIST_P256 SIGN_VERIFY key.
type KMS struct {
	client Client
	keyARN string
	pub    *ecdsa.PublicKey
	kid    string
}

// NewKMS fetches and checks the key's public half.
func NewKMS(ctx context.Context, client Client, keyARN string) (*KMS, error) {
	out, err := client.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(keyARN)})
	if err != nil {
		return nil, fmt.Errorf("kmssign: GetPublicKey: %w", err)
	}
	if out.KeySpec != types.KeySpecEccNistP256 || out.KeyUsage != types.KeyUsageTypeSignVerify {
		return nil, fmt.Errorf("kmssign: key must be ECC_NIST_P256 for SIGN_VERIFY (is %s, %s)", out.KeySpec, out.KeyUsage)
	}
	pub, err := parseP256(out.PublicKey)
	if err != nil {
		return nil, err
	}
	return &KMS{client: client, keyARN: keyARN, pub: pub, kid: Thumbprint(pub)}, nil
}

// Sign implements dsse.Signer.
func (k *KMS) Sign(ctx context.Context, pae []byte) ([]byte, string, error) {
	digest := sha256.Sum256(pae)
	out, err := k.client.Sign(ctx, &kms.SignInput{KeyId: aws.String(k.keyARN), Message: digest[:],
		MessageType: types.MessageTypeDigest, SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256})
	if err != nil {
		return nil, "", fmt.Errorf("kmssign: Sign: %w", err)
	}
	return out.Signature, k.kid, nil
}

func (k *KMS) PublicKey() *ecdsa.PublicKey { return k.pub }
func (k *KMS) KeyID() string               { return k.kid }
func (k *KMS) KeyRef() string              { return k.keyARN }

// Local signs with a P-256 private key from PEM (PKCS#8 or SEC 1).
type Local struct {
	*dsse.ECDSAP256Signer
	pub *ecdsa.PublicKey
	kid string
}

// NewLocal parses a P-256 private key.
func NewLocal(pemBytes []byte) (*Local, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("kmssign: the local signing key is not PEM")
	}
	var key *ecdsa.PrivateKey
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, _ = k.(*ecdsa.PrivateKey)
	} else if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		key = k
	}
	if key == nil || key.Curve != elliptic.P256() {
		return nil, errors.New("kmssign: the local signing key must be an ECDSA P-256 private key")
	}
	kid := Thumbprint(&key.PublicKey)
	s, err := dsse.NewECDSAP256Signer(key, kid)
	if err != nil {
		return nil, err
	}
	return &Local{ECDSAP256Signer: s, pub: &key.PublicKey, kid: kid}, nil
}

func (l *Local) PublicKey() *ecdsa.PublicKey { return l.pub }
func (l *Local) KeyID() string               { return l.kid }
func (l *Local) KeyRef() string              { return "local:" + l.kid }

func parseP256(der []byte) (*ecdsa.PublicKey, error) {
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("kmssign: public key: %w", err)
	}
	pub, ok := k.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("kmssign: public key is not P-256")
	}
	return pub, nil
}

func coords(pub *ecdsa.PublicKey) (x, y string) {
	b, _ := pub.ECDH() // validates the point; Bytes() is 0x04 || X || Y
	raw := b.Bytes()
	enc := base64.RawURLEncoding
	return enc.EncodeToString(raw[1:33]), enc.EncodeToString(raw[33:65])
}

// Thumbprint is the RFC 7638 JWK thumbprint of a P-256 key (base64url of the
// SHA-256 of {"crv","kty","x","y"} in that order): a stable kid for the key.
func Thumbprint(pub *ecdsa.PublicKey) string {
	x, y := coords(pub)
	sum := sha256.Sum256([]byte(`{"crv":"P-256","kty":"EC","x":"` + x + `","y":"` + y + `"}`))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// JWK is the key's public JWK (ES256), as the verifiers read it.
func JWK(pub *ecdsa.PublicKey, kid string) map[string]string {
	x, y := coords(pub)
	return map[string]string{"kty": "EC", "crv": "P-256", "kid": kid, "use": "sig", "alg": "ES256", "x": x, "y": y}
}

// JWKS is a key set of one key.
func JWKS(pub *ecdsa.PublicKey, kid string) []byte {
	b, _ := json.Marshal(map[string]any{"keys": []any{JWK(pub, kid)}})
	return b
}
