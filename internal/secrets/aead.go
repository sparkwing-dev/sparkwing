package secrets

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	envelopePrefix      = "enc:v1:"
	envelopePrefixBound = "enc:v2:"
)

const KeySize = chacha20poly1305.KeySize

type Cipher struct {
	aead cipher.AEAD
}

func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secrets cipher: key must be %d bytes, got %d", KeySize, len(key))
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("secrets cipher: init: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Seal(plain string) (string, error) {
	return c.seal(plain, envelopePrefix, nil)
}

// SealBound seals plain with the row fields that decide access to the
// secret as additional authenticated data: its name, its owning
// repository (empty for an unscoped secret), whether an unscoped row
// answers every run, and whether the value is redacted in run output.
// The envelope opens only under that same combination.
func (c *Cipher) SealBound(name, repo string, shared, masked bool, plain string) (string, error) {
	return c.seal(plain, envelopePrefixBound, boundAAD(name, repo, shared, masked))
}

func (c *Cipher) seal(plain, prefix string, aad []byte) (string, error) {
	if c == nil {
		return "", errors.New("secrets cipher: nil receiver")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secrets cipher: nonce: %w", err)
	}
	ct := c.aead.Seal(nil, nonce, []byte(plain), aad)
	envelope := append(nonce, ct...)
	return prefix + base64.StdEncoding.EncodeToString(envelope), nil
}

func (c *Cipher) Open(envelope string) (string, error) {
	if strings.HasPrefix(envelope, envelopePrefixBound) {
		return "", errors.New("secrets cipher: envelope is bound to a secret name and repository; open it with those")
	}
	return c.open(envelope, envelopePrefix, nil)
}

// OpenBound decrypts an envelope sealed for this combination of name,
// repo, shared and masked. Envelopes written before binding carry no
// additional data and open unchanged.
func (c *Cipher) OpenBound(name, repo string, shared, masked bool, envelope string) (string, error) {
	if strings.HasPrefix(envelope, envelopePrefixBound) {
		return c.open(envelope, envelopePrefixBound, boundAAD(name, repo, shared, masked))
	}
	return c.open(envelope, envelopePrefix, nil)
}

// safety: length prefixes keep one field from spelling another, so the binding needs no name rule to hold.
func boundAAD(name, repo string, shared, masked bool) []byte {
	aad := make([]byte, 0, 18+len(name)+len(repo))
	aad = appendBoundField(aad, name)
	aad = appendBoundField(aad, repo)
	return append(aad, boundFlag(shared), boundFlag(masked))
}

func appendBoundField(dst []byte, field string) []byte {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(field)))
	dst = append(dst, n[:]...)
	return append(dst, field...)
}

func boundFlag(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func (c *Cipher) open(envelope, prefix string, aad []byte) (string, error) {
	if c == nil {
		return "", errors.New("secrets cipher: no key configured")
	}
	if !strings.HasPrefix(envelope, prefix) {
		return "", errors.New("secrets cipher: value is not sealed")
	}
	body := strings.TrimPrefix(envelope, prefix)
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("secrets cipher: bad envelope encoding: %w", err)
	}
	nsz := c.aead.NonceSize()
	if len(raw) < nsz+c.aead.Overhead() {
		return "", errors.New("secrets cipher: envelope too short")
	}
	nonce, ct := raw[:nsz], raw[nsz:]
	plain, err := c.aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return "", fmt.Errorf("secrets cipher: open: %w", err)
	}
	return string(plain), nil
}

func IsEncrypted(v string) bool {
	return strings.HasPrefix(v, envelopePrefix) || IsBound(v)
}

// IsBound reports whether v is an envelope sealed to the row it
// belongs to. An encrypted value that is not bound predates binding
// and can still be moved onto another row, so operators can find such
// rows and readers can rebind them.
func IsBound(v string) bool {
	return strings.HasPrefix(v, envelopePrefixBound)
}

func DecodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("secrets cipher: key is empty")
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		if alt, alterr := base64.URLEncoding.DecodeString(s); alterr == nil {
			raw = alt
		} else {
			return nil, fmt.Errorf("secrets cipher: decode key: %w", err)
		}
	}
	if len(raw) != KeySize {
		return nil, fmt.Errorf("secrets cipher: key must be %d bytes after base64 decode, got %d", KeySize, len(raw))
	}
	return raw, nil
}

func GenerateKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("secrets cipher: keygen: %w", err)
	}
	return key, nil
}
