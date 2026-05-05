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

// envelopePrefix marks a Sealed value on disk so the reader can tell
// encrypted rows from rows that predate this ticket. Bumping the
// version (v2, v3, ...) lets us evolve the format without forcing a
// bulk re-encrypt: each row carries enough self-description to be
// decoded by whichever code path matches its prefix.
const envelopePrefix = "enc:v1:"

// KeySize is the master-key length expected by NewCipher (XChaCha20-
// Poly1305 = 32 bytes). Operators generate one with
// `head -c 32 /dev/urandom | base64`.
const KeySize = chacha20poly1305.KeySize

// Cipher seals and opens secret values with XChaCha20-Poly1305 AEAD.
// One Cipher per controller process; cheap to create, immutable.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher constructs a Cipher from a 32-byte master key. The key
// stays in memory for the lifetime of the controller; we don't make
// any effort to scrub it (Go's GC + the memory guarantees of
// SQLite's value-copy model already make sweeper-leak risk low for
// this threat model).
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

// Seal encrypts plain and returns the storage-ready envelope. Empty
// plain is allowed (rare but valid: a secret deliberately set to "").
// Returns the envelope; never the raw ciphertext on its own.
func (c *Cipher) Seal(plain string) (string, error) {
	if c == nil {
		return "", errors.New("secrets cipher: nil receiver")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secrets cipher: nonce: %w", err)
	}
	ct := c.aead.Seal(nil, nonce, []byte(plain), nil)
	// Pack nonce || ciphertext+tag into one blob so the on-disk
	// shape is a single base64 string instead of two fields.
	envelope := append(nonce, ct...)
	return envelopePrefix + base64.StdEncoding.EncodeToString(envelope), nil
}

// Open decodes a value that may or may not be sealed. Rows without
// the envelope prefix are returned verbatim so existing plaintext
// data round-trips through encrypted readers. Rows with the prefix
// are decrypted; tampering trips the AEAD's authentication and
// produces a clear error.
func (c *Cipher) Open(envelope string) (string, error) {
	if !IsEncrypted(envelope) {
		return envelope, nil
	}
	if c == nil {
		return "", errors.New("secrets cipher: encrypted value but no key configured")
	}
	body := strings.TrimPrefix(envelope, envelopePrefix)
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("secrets cipher: bad envelope encoding: %w", err)
	}
	nsz := c.aead.NonceSize()
	if len(raw) < nsz+c.aead.Overhead() {
		return "", errors.New("secrets cipher: envelope too short")
	}
	nonce, ct := raw[:nsz], raw[nsz:]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("secrets cipher: open: %w", err)
	}
	return string(plain), nil
}

// IsEncrypted reports whether v is in the sealed envelope shape.
// Cheap prefix check; safe to call without a Cipher.
func IsEncrypted(v string) bool {
	return strings.HasPrefix(v, envelopePrefix)
}

// DecodeKey parses a base64-encoded 32-byte key (the format the
// SPARKWING_SECRETS_KEY env var carries). Whitespace and trailing
// newlines are tolerated so operators can pipe `... | base64`
// without worrying about exact formatting.
func DecodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("secrets cipher: key is empty")
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// Try URL-safe encoding too -- some operator workflows pipe
		// through tools that prefer it (e.g., openssl rand -base64
		// vs head -c 32 ... | base64 -w0).
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

// GenerateKey returns a fresh 32-byte master key suitable for use
// with NewCipher. Convenience for tests + a future `sparkwing
// secrets keygen` subcommand.
func GenerateKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("secrets cipher: keygen: %w", err)
	}
	return key, nil
}
