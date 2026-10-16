// Package secretbox seals the credentials vendors give Plumbline for their own
// accounts elsewhere (D-049): AES-256-GCM under a 32-byte server key
// (INTEGRATION_KEY), with additional data that binds each sealed value to the
// row and tenant it belongs to, so a value copied to another row or tenant
// does not open. The key never leaves the process; only its ID (a digest) is
// stored beside what it sealed.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// Box seals and opens with one key.
type Box struct {
	aead cipher.AEAD
	id   string
}

// ErrOpen is any failure to open: wrong key, wrong binding, or tampering.
var ErrOpen = errors.New("secretbox: cannot open")

// FromBase64 reads a key written as standard or URL base64 of 32 bytes
// (`openssl rand -base64 32`).
func FromBase64(s string) (*Box, error) {
	s = strings.TrimSpace(s)
	var key []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err = enc.DecodeString(s); err == nil {
			break
		}
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("secretbox: the key must be 32 bytes in base64 (openssl rand -base64 32)")
	}
	return New(key)
}

// New makes a box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("secretbox: the key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte("plumbline.secretbox.key-id:"), key...))
	return &Box{aead: aead, id: "k1:" + hex.EncodeToString(sum[:6])}, nil
}

// KeyID names the key without revealing it.
func (b *Box) KeyID() string { return b.id }

// Seal returns nonce || ciphertext, bound to binding.
func (b *Box) Seal(plain []byte, binding string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic("secretbox: no randomness: " + err.Error())
	}
	return b.aead.Seal(nonce, nonce, plain, []byte(binding))
}

// Open reverses Seal for the same binding.
func (b *Box) Open(sealed []byte, binding string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n+b.aead.Overhead() {
		return nil, ErrOpen
	}
	out, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(binding))
	if err != nil {
		return nil, ErrOpen
	}
	return out, nil
}
