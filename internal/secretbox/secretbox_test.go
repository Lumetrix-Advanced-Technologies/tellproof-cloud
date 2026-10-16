package secretbox

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func box(t *testing.T) (*Box, string) {
	t.Helper()
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	s := base64.StdEncoding.EncodeToString(k)
	b, err := FromBase64(s)
	if err != nil {
		t.Fatal(err)
	}
	return b, s
}

func TestSealOpenAndBinding(t *testing.T) {
	b, _ := box(t)
	plain := []byte(`{"client_secret":"vanta-secret-value"}`)
	sealed := b.Seal(plain, "tenant-a/row-1")
	if bytes.Contains(sealed, []byte("vanta-secret-value")) {
		t.Fatal("the sealed value contains the secret")
	}
	if again := b.Seal(plain, "tenant-a/row-1"); bytes.Equal(again, sealed) {
		t.Fatal("two seals are identical: the nonce is not random")
	}
	got, err := b.Open(sealed, "tenant-a/row-1")
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := b.Open(sealed, "tenant-b/row-1"); !errors.Is(err, ErrOpen) {
		t.Fatalf("opened under another binding: %v", err)
	}
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := b.Open(tampered, "tenant-a/row-1"); !errors.Is(err, ErrOpen) {
		t.Fatalf("opened a tampered value: %v", err)
	}
	if _, err := b.Open(sealed[:5], "tenant-a/row-1"); !errors.Is(err, ErrOpen) {
		t.Fatalf("opened a short value: %v", err)
	}
	other, _ := box(t)
	if _, err := other.Open(sealed, "tenant-a/row-1"); !errors.Is(err, ErrOpen) {
		t.Fatalf("opened with another key: %v", err)
	}
	if other.KeyID() == b.KeyID() || !strings.HasPrefix(b.KeyID(), "k1:") {
		t.Fatalf("key IDs %q and %q", b.KeyID(), other.KeyID())
	}
}

func TestKeyFormats(t *testing.T) {
	_, s := box(t)
	same, _ := FromBase64(s)
	url, err := FromBase64(strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(s), "="))
	if err != nil || url.KeyID() != same.KeyID() {
		t.Fatalf("URL base64 of the same key: %v", err)
	}
	for _, bad := range []string{"", "short", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := FromBase64(bad); err == nil {
			t.Errorf("accepted key %q", bad)
		}
	}
}
