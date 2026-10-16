package kmssign

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/dsse"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/pack"
)

// fakeKMS signs digests with a local key and checks the request as KMS would.
type fakeKMS struct {
	key     *ecdsa.PrivateKey
	spec    types.KeySpec
	signed  int
	lastLen int
}

func (f *fakeKMS) GetPublicKey(_ context.Context, in *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	der, _ := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	return &kms.GetPublicKeyOutput{KeyId: in.KeyId, PublicKey: der, KeySpec: f.spec, KeyUsage: types.KeyUsageTypeSignVerify}, nil
}

func (f *fakeKMS) Sign(_ context.Context, in *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	f.signed++
	f.lastLen = len(in.Message)
	if in.MessageType != types.MessageTypeDigest || in.SigningAlgorithm != types.SigningAlgorithmSpecEcdsaSha256 {
		return nil, errors.New("wrong message type or algorithm")
	}
	if len(in.Message) != 32 {
		return nil, errors.New("a DIGEST message must be 32 bytes for ECDSA_SHA_256")
	}
	sig, err := ecdsa.SignASN1(rand.Reader, f.key, in.Message)
	return &kms.SignOutput{Signature: sig, KeyId: in.KeyId}, err
}

func TestKMSSignsTheDigestAndVerifies(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f := &fakeKMS{key: key, spec: types.KeySpecEccNistP256}
	s, err := NewKMS(context.Background(), f, "arn:aws:kms:ap-southeast-2:111122223333:key/test")
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 20000) // far beyond KMS's 4,096-byte message cap
	env, err := dsse.Sign(context.Background(), s, "application/vnd.in-toto+json", payload)
	if err != nil {
		t.Fatal(err)
	}
	if f.lastLen != 32 {
		t.Errorf("sent %d bytes to KMS", f.lastLen)
	}
	keys, err := pack.ParseJWKS(JWKS(s.PublicKey(), s.KeyID()))
	if err != nil {
		t.Fatal(err)
	}
	if _, kid, err := dsse.Verify(env, "application/vnd.in-toto+json", keys); err != nil || kid != s.KeyID() {
		t.Errorf("verify: %v %s", err, kid)
	}
	if s.KeyRef() != "arn:aws:kms:ap-southeast-2:111122223333:key/test" {
		t.Errorf("key ref %s", s.KeyRef())
	}
	if _, err := NewKMS(context.Background(), &fakeKMS{key: key, spec: types.KeySpecRsa2048}, "k"); err == nil {
		t.Error("an RSA key was accepted")
	}
}

func TestLocalSigner(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	l, err := NewLocal(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	env, err := dsse.Sign(context.Background(), l, "t", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := pack.ParseJWKS(JWKS(l.PublicKey(), l.KeyID()))
	if _, _, err := dsse.Verify(env, "t", keys); err != nil {
		t.Error(err)
	}
	sec1, _ := x509.MarshalECPrivateKey(key)
	l2, err := NewLocal(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1}))
	if err != nil || l2.KeyID() != l.KeyID() || l.KeyRef() != "local:"+l.KeyID() {
		t.Errorf("SEC 1 key: %v", err)
	}
	if _, err := NewLocal([]byte("not pem")); err == nil {
		t.Error("garbage accepted")
	}
}

// RFC 7638: base64url(SHA-256) of the required members in lexicographic order
// with no whitespace. json.Marshal of a map sorts keys, an independent way to
// build the same bytes.
func TestThumbprintFollowsRFC7638(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	j := JWK(&key.PublicKey, "ignored")
	b, _ := json.Marshal(map[string]string{"crv": j["crv"], "kty": j["kty"], "x": j["x"], "y": j["y"]})
	sum := sha256.Sum256(b)
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); Thumbprint(&key.PublicKey) != want {
		t.Errorf("thumbprint %s, want %s", Thumbprint(&key.PublicKey), want)
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if Thumbprint(&key.PublicKey) == Thumbprint(&other.PublicKey) {
		t.Error("two keys share a thumbprint")
	}
}
