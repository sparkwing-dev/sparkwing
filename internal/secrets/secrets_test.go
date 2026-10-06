package secrets

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
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

func TestCached_HitsSourceOnce(t *testing.T) {
	var calls int32
	src := SourceFunc(func(name string) (string, bool, error) {
		atomic.AddInt32(&calls, 1)
		return "val-" + name, true, nil
	})
	masker := NewMasker()
	c := NewCached(src, masker)

	for range 5 {
		v, masked, err := c.Resolve(context.Background(), "FOO")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if v != "val-FOO" {
			t.Fatalf("v = %q", v)
		}
		if !masked {
			t.Fatalf("Resolve masked = false, want true")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("source called %d times, want 1 (cache miss)", got)
	}
	if !contains(masker.Values(), "val-FOO") {
		t.Fatalf("masker did not register the resolved value")
	}
}

func TestCached_DoesNotCacheErrors(t *testing.T) {
	var calls int32
	src := SourceFunc(func(name string) (string, bool, error) {
		atomic.AddInt32(&calls, 1)
		return "", false, errors.New("source unreachable")
	})
	c := NewCached(src, NewMasker())

	for range 3 {
		if _, _, err := c.Resolve(context.Background(), "FOO"); err == nil {
			t.Fatal("expected error")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("source called %d times, want 3 (errors must not cache)", got)
	}
}

func TestCached_DoesNotMaskUnmaskedEntries(t *testing.T) {
	src := SourceFunc(func(name string) (string, bool, error) {
		return "us-east-1", false, nil
	})
	masker := NewMasker()
	c := NewCached(src, masker)
	v, masked, err := c.Resolve(context.Background(), "REGION")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if v != "us-east-1" || masked {
		t.Fatalf("Resolve = %q, masked=%v; want us-east-1, masked=false", v, masked)
	}
	if len(masker.Values()) != 0 {
		t.Fatalf("masker registered unmasked entry: %v", masker.Values())
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
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
	bound, err := oldCipher.SealBound("acme", "TOKEN", "acme/web", false, true, "bound-value")
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
	got, err = rotating.OpenBound("acme", "TOKEN", "acme/web", false, true, bound)
	if err != nil {
		t.Fatalf("OpenBound under the previous key: %v", err)
	}
	if got != "bound-value" {
		t.Fatalf("OpenBound = %q, want bound-value", got)
	}
	if _, err := rotating.OpenBound("acme", "OTHER", "acme/web", false, true, bound); err == nil {
		t.Fatal("the previous key opened an envelope bound to another row")
	}

	resealed, err := rotating.SealBound("acme", "TOKEN", "acme/web", false, true, "bound-value")
	if err != nil {
		t.Fatalf("SealBound: %v", err)
	}
	if _, err := current.OpenBound("acme", "TOKEN", "acme/web", false, true, resealed); err != nil {
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

func TestMaskerMultilineComponentsKeepLiteralMatchingPolicy(t *testing.T) {
	m := NewMasker()
	value := "xy\n \r\nlonger-private-xy\n"
	m.Register(value)
	before := m.Values()
	m.Register(value)
	if !slices.Equal(before, m.Values()) || !slices.Contains(before, "xy") || !slices.Contains(before, "longer-private-xy") || slices.Contains(before, " ") {
		t.Fatalf("registration not deduplicated: %q", m.Values())
	}
	if !slices.Contains(before, value) {
		t.Fatal("whole value not retained")
	}
	if got := m.Mask("longer-private-xy xy ordinary"); got != "*** *** ordinary" {
		t.Fatalf("longest/literal matching=%q", got)
	}
	if got := m.Mask("ordinary xy text"); got != "ordinary *** text" {
		t.Fatalf("short component rule=%q", got)
	}
	if got := m.Mask(" \r"); got != " \r" {
		t.Fatal("blank component became a mask pattern")
	}
	spaces := NewMasker()
	spaces.Register(" left \r\nright\r\n")
	if spaces.Mask("left") != "left" || spaces.Mask(" left ") != "***" || spaces.Mask("right") != "***" {
		t.Fatal("line whitespace was changed beyond CRLF normalization")
	}
	if m.Mask("longer-private-") != "longer-private-" {
		t.Fatal("partial-prefix matching was introduced")
	}
}
