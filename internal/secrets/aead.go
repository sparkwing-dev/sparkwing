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
	envelopePrefix       = "enc:v1:"
	envelopePrefixLegacy = "enc:v2:"
	// BoundPrefix starts every envelope sealed to the team that owns its row.
	// A stored value without it is plaintext or predates team binding.
	BoundPrefix = "enc:v3:"
)

// safety: the v3 binding opens with a label no v2 binding can spell, so an
// envelope relabelled from one version to the other never authenticates.
const boundAADLabel = "sparkwing/secret/v3\x00"

// ErrLegacyEnvelope is what [Cipher.OpenBound] returns for an envelope sealed
// before the owning team joined the binding. Only [Cipher.OpenLegacy] opens
// one, and the controller calls that once, to reseal it at startup.
var ErrLegacyEnvelope = errors.New("secrets cipher: envelope predates team binding and must be resealed")

const KeySize = chacha20poly1305.KeySize

type Cipher struct {
	aead cipher.AEAD
	// safety: read-only keys an envelope may still carry; sealing always uses aead.
	previous []cipher.AEAD
}

func NewCipher(key []byte) (*Cipher, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// NewCipherWithPrevious returns a cipher that seals under key and opens an
// envelope under key first and previous second. It is the read path a key
// change needs: values sealed under the old key keep opening until every
// one of them has been re-encrypted, after which previous can be dropped.
func NewCipherWithPrevious(key, previous []byte) (*Cipher, error) {
	c, err := NewCipher(key)
	if err != nil {
		return nil, err
	}
	prev, err := newAEAD(previous)
	if err != nil {
		return nil, fmt.Errorf("previous key: %w", err)
	}
	c.previous = []cipher.AEAD{prev}
	return c, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secrets cipher: key must be %d bytes, got %d", KeySize, len(key))
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("secrets cipher: init: %w", err)
	}
	return aead, nil
}

func (c *Cipher) Seal(plain string) (string, error) {
	return c.seal(plain, envelopePrefix, nil)
}

// SealBound seals plain with the row fields that decide access to the
// secret as additional authenticated data: the team that owns the row, its
// name, its owning scope (the owning pipeline, empty for an unscoped
// secret), whether an unscoped row answers every run, and whether the value
// is redacted in run output. The envelope opens only under that same
// combination, so one team's envelope copied into another team's row does
// not open.
func (c *Cipher) SealBound(team, name, scope string, shared, masked bool, plain string) (string, error) {
	return c.seal(plain, BoundPrefix, boundAAD(team, name, scope, shared, masked))
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
	if strings.HasPrefix(envelope, BoundPrefix) || strings.HasPrefix(envelope, envelopePrefixLegacy) {
		return "", errors.New("secrets cipher: envelope is bound to a secret's row; open it with that binding")
	}
	return c.open(envelope, envelopePrefix, nil)
}

// OpenBound decrypts an envelope sealed by [Cipher.SealBound] for this
// combination of team, name, scope, shared and masked. It refuses an
// envelope sealed before team binding with [ErrLegacyEnvelope].
func (c *Cipher) OpenBound(team, name, scope string, shared, masked bool, envelope string) (string, error) {
	if strings.HasPrefix(envelope, BoundPrefix) {
		return c.open(envelope, BoundPrefix, boundAAD(team, name, scope, shared, masked))
	}
	if IsEncrypted(envelope) {
		return "", ErrLegacyEnvelope
	}
	return "", errors.New("secrets cipher: value is not sealed")
}

// OpenLegacy decrypts an envelope sealed before the team joined the binding:
// one bound to name, scope, shared and masked alone, or one bound to nothing.
// Such an envelope can be moved between teams undetected, so its only caller
// is the one that reseals it under [Cipher.SealBound].
func (c *Cipher) OpenLegacy(name, scope string, shared, masked bool, envelope string) (string, error) {
	if strings.HasPrefix(envelope, envelopePrefixLegacy) {
		return c.open(envelope, envelopePrefixLegacy, legacyAAD(name, scope, shared, masked))
	}
	return c.open(envelope, envelopePrefix, nil)
}

// safety: length prefixes keep one field from spelling another, so the binding needs no name rule to hold.
func boundAAD(team, name, scope string, shared, masked bool) []byte {
	aad := make([]byte, 0, len(boundAADLabel)+26+len(team)+len(name)+len(scope))
	aad = append(aad, boundAADLabel...)
	aad = appendBoundField(aad, team)
	aad = appendBoundField(aad, name)
	aad = appendBoundField(aad, scope)
	return append(aad, boundFlag(shared), boundFlag(masked))
}

func legacyAAD(name, scope string, shared, masked bool) []byte {
	aad := make([]byte, 0, 18+len(name)+len(scope))
	aad = appendBoundField(aad, name)
	aad = appendBoundField(aad, scope)
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
	if err == nil {
		return string(plain), nil
	}
	for _, prev := range c.previous {
		if plain, perr := prev.Open(nil, nonce, ct, aad); perr == nil {
			return string(plain), nil
		}
	}
	return "", fmt.Errorf("secrets cipher: open: %w", err)
}

func IsEncrypted(v string) bool {
	return strings.HasPrefix(v, envelopePrefix) || strings.HasPrefix(v, envelopePrefixLegacy) || IsBound(v)
}

// IsBound reports whether v is an envelope sealed to the team and row it
// belongs to. An encrypted value that is not bound predates team binding
// and can be moved onto another team's row, so the controller reseals every
// such row when it starts with a key.
func IsBound(v string) bool {
	return strings.HasPrefix(v, BoundPrefix)
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
