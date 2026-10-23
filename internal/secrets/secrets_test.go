package secrets

import (
	"slices"
	"testing"
)

func TestMasker_RegisterAndMask(t *testing.T) {
	m := NewMasker()
	m.Register("supersecret")
	m.Register("another")
	m.Register("")
	got := m.Mask("token=supersecret other=another visible")
	if got != "token=*** other=*** visible" {
		t.Fatalf("Mask = %q", got)
	}
	if vs := m.Values(); !slices.Contains(vs, "supersecret") || !slices.Contains(vs, "another") || slices.Contains(vs, "") {
		t.Fatalf("Values = %q", vs)
	}
}

func TestContainsSecretChecksIdentifiers(t *testing.T) {
	m := NewMasker()
	if m.ContainsSecret("ordinary-job") {
		t.Fatal("an empty registry matched an identifier")
	}
	m.Register("private/value")
	for _, identifier := range []string{"job-private/value", "job-private%2Fvalue"} {
		if !m.ContainsSecret(identifier) {
			t.Fatalf("registered secret was accepted in %q", identifier)
		}
	}
	if m.ContainsSecret("ordinary-job") {
		t.Fatal("unrelated identifier matched a registered secret")
	}
}

func TestCipher_PreviousKeyOpensOldEnvelopes(t *testing.T) {
	oldKey, err := GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	newKey, err := GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	oldCipher, err := NewCipher(oldKey)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	unbound, err := oldCipher.Seal("plain-value")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	bound, err := oldCipher.SealBound(PurposeSecret, "acme", "TOKEN", "acme/web", false, true, "bound-value")
	if err != nil {
		t.Fatalf("SealBound: %v", err)
	}

	current, err := NewCipher(newKey)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	if _, err := current.Open(unbound); err == nil {
		t.Fatal("the new key alone opened an envelope sealed under the old one")
	}

	rotating, err := NewCipherWithPrevious(newKey, oldKey)
	if err != nil {
		t.Fatalf("NewCipherWithPrevious: %v", err)
	}
	got, err := rotating.Open(unbound)
	if err != nil {
		t.Fatalf("Open under the previous key: %v", err)
	}
	if got != "plain-value" {
		t.Fatalf("Open = %q, want plain-value", got)
	}
	got, err = rotating.OpenBound(PurposeSecret, "acme", "TOKEN", "acme/web", false, true, bound)
	if err != nil {
		t.Fatalf("OpenBound under the previous key: %v", err)
	}
	if got != "bound-value" {
		t.Fatalf("OpenBound = %q, want bound-value", got)
	}
	if _, err := rotating.OpenBound(PurposeSecret, "acme", "OTHER", "acme/web", false, true, bound); err == nil {
		t.Fatal("the previous key opened an envelope bound to another row")
	}

	resealed, err := rotating.SealBound(PurposeSecret, "acme", "TOKEN", "acme/web", false, true, "bound-value")
	if err != nil {
		t.Fatalf("SealBound: %v", err)
	}
	if _, err := current.OpenBound(PurposeSecret, "acme", "TOKEN", "acme/web", false, true, resealed); err != nil {
		t.Fatalf("a rotating cipher sealed under something other than the current key: %v", err)
	}
}

func TestNewCipherWithPrevious_RejectsAShortPreviousKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if _, err := NewCipherWithPrevious(key, []byte("too short")); err == nil {
		t.Fatal("NewCipherWithPrevious accepted a previous key of the wrong length")
	}
}

func TestMaskerSkipsShortValuesAndEncodings(t *testing.T) {
	m := NewMasker()
	for _, value := range []string{"abc", "ééé", ""} {
		if err := m.Register(value); err != nil {
			t.Fatalf("Register(%q) = %v", value, err)
		}
		for _, text := range append(encodedForms(value), value) {
			if got := m.Mask(text); got != text {
				t.Fatalf("short secret spelling %q masked to %q", text, got)
			}
		}
	}
	if len(m.Values()) != 0 {
		t.Fatalf("short values registered: %q", m.Values())
	}
	if err := m.Register("éééé"); err != nil {
		t.Fatal(err)
	}
}

func TestMaskerAcceptsMarkerContainingSecrets(t *testing.T) {
	for _, value := range []string{"long***secret", "****", "x***"} {
		m := NewMasker()
		if err := m.Register(value); err != nil {
			t.Fatal(err)
		}
		stream := m.Stream()
		got := stream.Mask(value[:2]) + stream.Mask(value[2:]) + stream.Flush()
		if got != "***" {
			t.Fatalf("%q masked to %q", value, got)
		}
	}
}
