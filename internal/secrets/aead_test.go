package secrets

import (
	"encoding/base64"
	"errors"
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

func TestCipher_BindsTeamNameAndRepo(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)

	env, err := c.SealBound("acme", "aws/prod/token", "acme/api", false, true, "bound")
	if err != nil {
		t.Fatalf("SealBound: %v", err)
	}
	if !strings.HasPrefix(env, BoundPrefix) {
		t.Fatalf("SealBound envelope = %q, want a %s prefix", env, BoundPrefix)
	}
	if !IsEncrypted(env) {
		t.Fatalf("IsEncrypted(%q) = false, want true", env)
	}
	if !IsBound(env) {
		t.Fatalf("IsBound(%q) = false, want true", env)
	}
	got, err := c.OpenBound("acme", "aws/prod/token", "acme/api", false, true, env)
	if err != nil {
		t.Fatalf("OpenBound: %v", err)
	}
	if got != "bound" {
		t.Fatalf("OpenBound = %q, want bound", got)
	}

	for _, c2 := range []struct {
		label, team, name, repo string
		shared, masked          bool
	}{
		{"same row in another team", "globex", "aws/prod/token", "acme/api", false, true},
		{"same row with no team", "", "aws/prod/token", "acme/api", false, true},
		{"team shifted into the name", "acm", "eaws/prod/token", "acme/api", false, true},
		{"other name, same repo", "acme", "aws/dev/token", "acme/api", false, true},
		{"same name, other repo", "acme", "aws/prod/token", "acme/web", false, true},
		{"same name, unscoped row", "acme", "aws/prod/token", "", false, true},
		{"fields shifted across the split point", "acme", "aws/prod/token\x00acme", "/api", false, true},
		{"shared flipped on", "acme", "aws/prod/token", "acme/api", true, true},
		{"masked flipped off", "acme", "aws/prod/token", "acme/api", false, false},
	} {
		if _, err := c.OpenBound(c2.team, c2.name, c2.repo, c2.shared, c2.masked, env); err == nil {
			t.Errorf("OpenBound accepted an envelope sealed elsewhere (%s)", c2.label)
		}
	}
	if _, err := c.Open(env); err == nil {
		t.Fatal("Open accepted a bound envelope without its binding")
	}
	if _, err := c.OpenLegacy("aws/prod/token", "acme/api", false, true, env); err == nil {
		t.Fatal("OpenLegacy accepted a team-bound envelope without its team")
	}
}

func TestCipher_BindsUnscopedRowToEmptyRepo(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)

	env, err := c.SealBound("default", "TOKEN", "", true, true, "unscoped")
	if err != nil {
		t.Fatalf("SealBound: %v", err)
	}
	got, err := c.OpenBound("default", "TOKEN", "", true, true, env)
	if err != nil {
		t.Fatalf("OpenBound: %v", err)
	}
	if got != "unscoped" {
		t.Fatalf("OpenBound = %q, want unscoped", got)
	}
	if _, err := c.OpenBound("default", "TOKEN", "acme/api", true, true, env); err == nil {
		t.Fatal("OpenBound accepted an unscoped envelope under a repository")
	}
}

// Envelopes sealed before the team joined the binding can be moved between
// teams undetected, so the read path refuses them and only OpenLegacy, which
// the controller calls to reseal them, opens them.
func TestCipher_OpenBoundRefusesEnvelopesSealedBeforeTeamBinding(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)

	unbound, err := c.Seal("v1 value")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(unbound, "enc:v1:") {
		t.Fatalf("Seal envelope = %q, want an enc:v1: prefix", unbound)
	}
	nameBound, err := c.seal("v2 value", envelopePrefixLegacy, legacyAAD("TOKEN", "acme/api", false, true))
	if err != nil {
		t.Fatalf("seal v2: %v", err)
	}
	for _, env := range []string{unbound, nameBound} {
		if IsBound(env) {
			t.Fatalf("IsBound(%q) = true, want false for an envelope sealed before team binding", env)
		}
		if !IsEncrypted(env) {
			t.Fatalf("IsEncrypted(%q) = false, want true", env)
		}
		if _, err := c.OpenBound("default", "TOKEN", "acme/api", false, true, env); !errors.Is(err, ErrLegacyEnvelope) {
			t.Fatalf("OpenBound(%.10s...) err = %v, want ErrLegacyEnvelope", env, err)
		}
	}

	got, err := c.OpenLegacy("TOKEN", "acme/api", false, true, unbound)
	if err != nil || got != "v1 value" {
		t.Fatalf("OpenLegacy(v1) = %q, %v; want v1 value", got, err)
	}
	got, err = c.OpenLegacy("TOKEN", "acme/api", false, true, nameBound)
	if err != nil || got != "v2 value" {
		t.Fatalf("OpenLegacy(v2) = %q, %v; want v2 value", got, err)
	}
	if _, err := c.OpenLegacy("OTHER", "acme/api", false, true, nameBound); err == nil {
		t.Fatal("OpenLegacy opened a v2 envelope under another name")
	}

	relabelled := BoundPrefix + strings.TrimPrefix(nameBound, envelopePrefixLegacy)
	if _, err := c.OpenBound("", "TOKEN", "acme/api", false, true, relabelled); err == nil {
		t.Fatal("OpenBound opened a v2 envelope relabelled as v3")
	}
}

func TestCipher_OpenBoundRejectsTampered(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)

	env, _ := c.SealBound("acme", "TOKEN", "acme/api", false, true, "hello")
	tampered := env[:len(env)-1] + "A"
	if tampered == env {
		tampered = env[:len(env)-1] + "B"
	}
	if _, err := c.OpenBound("acme", "TOKEN", "acme/api", false, true, tampered); err == nil {
		t.Fatal("OpenBound accepted a tampered envelope")
	}
	if _, err := c.OpenBound("acme", "TOKEN", "acme/api", false, true, "plain-no-prefix"); err == nil {
		t.Fatal("OpenBound accepted an unsealed value")
	}
}

func TestCipher_WrongKeyFailsToOpen(t *testing.T) {
	key, _ := GenerateKey()
	other, _ := GenerateKey()
	c, _ := NewCipher(key)
	wrong, _ := NewCipher(other)

	env, _ := c.SealBound("acme", "TOKEN", "", false, true, "hello")
	got, err := wrong.OpenBound("acme", "TOKEN", "", false, true, env)
	if err == nil {
		t.Fatalf("OpenBound under the wrong key returned %q with nil error", got)
	}
	if got != "" {
		t.Fatalf("OpenBound under the wrong key returned %q alongside its error", got)
	}
}

func TestCipher_OpenRejectsUnsealed(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)

	if _, err := c.Open("plain-no-prefix"); err == nil {
		t.Fatal("Open accepted an unsealed value")
	}
}

func TestCipher_OpenRejectsTampered(t *testing.T) {
	key, _ := GenerateKey()
	c, _ := NewCipher(key)

	env, _ := c.Seal("hello")
	tampered := env[:len(env)-1] + "A"
	if tampered == env {
		tampered = env[:len(env)-1] + "B"
	}
	if _, err := c.Open(tampered); err == nil {
		t.Fatal("Open accepted tampered envelope")
	}
}

func TestCipher_OpenWithoutKeyRejects(t *testing.T) {
	var nilC *Cipher
	if _, err := nilC.Open("enc:v1:somebody"); err == nil {
		t.Fatal("nil cipher must reject sealed values")
	}
	if _, err := nilC.Open("plain"); err == nil {
		t.Fatal("nil cipher must reject unsealed values")
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
	if _, err := DecodeKey("aGVsbG8="); err == nil {
		t.Fatal("short key must error")
	}
}

func encodeKey(t *testing.T, key []byte) string {
	t.Helper()
	return base64StdEncode(key)
}
