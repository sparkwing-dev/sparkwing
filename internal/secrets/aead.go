package secrets

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	envelopePrefix      = "enc:v1:"
	envelopePrefixNamed = "enc:v2:"
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

// SealNamed seals plain with name as additional authenticated data,
// so the envelope opens only under that name.
func (c *Cipher) SealNamed(name, plain string) (string, error) {
	return c.seal(plain, envelopePrefixNamed, []byte(name))
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
	if strings.HasPrefix(envelope, envelopePrefixNamed) {
		return "", errors.New("secrets cipher: envelope is bound to a secret name; open it with that name")
	}
	return c.open(envelope, envelopePrefix, nil)
}

// OpenNamed decrypts an envelope sealed under name. Envelopes written
// before name binding carry no additional data and open unchanged.
func (c *Cipher) OpenNamed(name, envelope string) (string, error) {
	if strings.HasPrefix(envelope, envelopePrefixNamed) {
		return c.open(envelope, envelopePrefixNamed, []byte(name))
	}
	return c.open(envelope, envelopePrefix, nil)
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
	return strings.HasPrefix(v, envelopePrefix) || strings.HasPrefix(v, envelopePrefixNamed)
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
