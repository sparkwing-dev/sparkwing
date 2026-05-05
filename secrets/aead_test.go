package secrets

import (
	"encoding/base64"
	"strings"
	"testing"
)

func base64StdEncode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func TestCipher_RoundTrip(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	c, err := NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}

	for _, plain := range []string{
		"abc123",
		"",
		"long\nvalue\nwith\nnewlines",
		strings.Repeat("x", 10000),
	} {
		env, err := c.Seal(plain)
		if err != nil {
			t.Fatalf("Seal(%q): %v", plain, err)
		}
		if !IsEncrypted(env) {
			t.Fatalf("Seal output missing prefix: %q", env)
		}
		got, err := c.Open(env)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got != plain {
			t.Fatalf("round-trip mismatch: got %q, want %q", got, plain)
		}
	}
}

func TestCipher_NonceVariesPerSeal(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)

	a, _ := c.Seal("same")
	b, _ := c.Seal("same")
	if a == b {
		t.Fatal("two seals of same plaintext produced identical envelopes (nonce reuse)")
	}
}

func TestCipher_OpenLeavesPlaintextAsIs(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)

	got, err := c.Open("plain-no-prefix")
	if err != nil {
		t.Fatalf("Open of unprefixed value: %v", err)
	}
	if got != "plain-no-prefix" {
		t.Fatalf("plaintext mutated: %q", got)
	}
}

func TestCipher_OpenRejectsTampered(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)

	env, _ := c.Seal("hello")
	// Flip the last base64 char to break the auth tag.
	tampered := env[:len(env)-1] + "A"
	if tampered == env {
		tampered = env[:len(env)-1] + "B"
	}
	if _, err := c.Open(tampered); err == nil {
		t.Fatal("Open accepted tampered envelope")
	}
}

func TestCipher_OpenWithoutKeyRejectsEncrypted(t *testing.T) {
	var nilC *Cipher
	if _, err := nilC.Open("enc:v1:somebody"); err == nil {
		t.Fatal("nil cipher must reject sealed values")
	}
	// But should pass plaintext through cleanly so a controller
	// without a key still serves pre-encryption rows.
	got, err := nilC.Open("plain")
	if err != nil {
		t.Fatalf("nil cipher unexpectedly errored on plaintext: %v", err)
	}
	if got != "plain" {
		t.Fatalf("got %q, want plain", got)
	}
}

func TestNewCipher_RejectsBadKey(t *testing.T) {
	if _, err := NewCipher(make([]byte, 16)); err == nil {
		t.Fatal("16-byte key must be rejected")
	}
}

func TestDecodeKey(t *testing.T) {
	key, _ := GenerateKey()
	c, err := NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	// Round-trip via base64 the way the env var carries it.
	env, _ := c.Seal("checkpoint")
	encoded := encodeKey(t, key)
	decoded, err := DecodeKey(encoded)
	if err != nil {
		t.Fatalf("DecodeKey: %v", err)
	}
	c2, err := NewCipher(decoded)
	if err != nil {
		t.Fatalf("NewCipher 2: %v", err)
	}
	got, err := c2.Open(env)
	if err != nil {
		t.Fatalf("Open with decoded key: %v", err)
	}
	if got != "checkpoint" {
		t.Fatalf("got %q", got)
	}

	if _, err := DecodeKey(""); err == nil {
		t.Fatal("empty key must error")
	}
	if _, err := DecodeKey("not-base64!"); err == nil {
		t.Fatal("malformed base64 must error")
	}
	// Wrong length after decode.
	if _, err := DecodeKey("aGVsbG8="); err == nil {
		t.Fatal("short key must error")
	}
}

func encodeKey(t *testing.T, key []byte) string {
	t.Helper()
	// Mirror what a shell pipeline produces.
	return base64StdEncode(key)
}
