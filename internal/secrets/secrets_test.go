package secrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDotenvSource_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.env")

	if err := WriteDotenvEntry(path, "TOKEN", "abc123"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := WriteDotenvEntry(path, "WITH_SPACE", "one two three"); err != nil {
		t.Fatalf("write quoted: %v", err)
	}

	// chmod must clamp to 0600 regardless of pre-existing perms.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perms = %o, want 0600", info.Mode().Perm())
	}

	// Only secrets file populated; config path defaults to a missing
	// file under the test HOME, treated as empty.
	t.Setenv("HOME", dir)
	src := NewDotenvSource(path)
	got, masked, err := src.Read("TOKEN")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "abc123" || !masked {
		t.Fatalf("Read = (%q, masked=%v), want (abc123, true)", got, masked)
	}
	got, masked, err = src.Read("WITH_SPACE")
	if err != nil {
		t.Fatalf("Read quoted: %v", err)
	}
	if got != "one two three" || !masked {
		t.Fatalf("Read quoted = (%q, masked=%v)", got, masked)
	}
}

// : config.env entries surface as masked=false; on collision
// the plain entry wins so an explicit "I marked this as plain" beats
// the safe default.
func TestDotenvSource_PlainAndMasked(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, "secrets.env")
	configPath := filepath.Join(dir, "config.env")
	if err := WriteDotenvEntry(secretsPath, "TOKEN", "abc123"); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := WriteDotenvEntry(configPath, "REGION", "us-east-1"); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Collision: same name in both files, plain wins.
	if err := WriteDotenvEntry(secretsPath, "MODE", "masked-loses"); err != nil {
		t.Fatalf("write masked: %v", err)
	}
	if err := WriteDotenvEntry(configPath, "MODE", "plain-wins"); err != nil {
		t.Fatalf("write plain: %v", err)
	}

	src := NewDotenvSourcePaths(secretsPath, configPath)
	v, masked, err := src.Read("TOKEN")
	if err != nil || v != "abc123" || !masked {
		t.Fatalf("TOKEN: (%q, masked=%v, err=%v)", v, masked, err)
	}
	v, masked, err = src.Read("REGION")
	if err != nil || v != "us-east-1" || masked {
		t.Fatalf("REGION: (%q, masked=%v, err=%v)", v, masked, err)
	}
	v, masked, err = src.Read("MODE")
	if err != nil || v != "plain-wins" || masked {
		t.Fatalf("MODE: (%q, masked=%v, err=%v); plain should win on collision", v, masked, err)
	}
}

func TestDotenvSource_MissingFileMeansEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	src := NewDotenvSource(filepath.Join(dir, "absent.env"))
	_, _, err := src.Read("FOO")
	if !errors.Is(err, ErrSecretMissing) {
		t.Fatalf("Read on missing file: err = %v, want ErrSecretMissing", err)
	}
}

func TestDotenvSource_MalformedLineErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	path := filepath.Join(dir, "bad.env")
	if err := os.WriteFile(path, []byte("notakeyvalueline\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	src := NewDotenvSource(path)
	_, _, err := src.Read("FOO")
	if err == nil || strings.Contains(err.Error(), "ErrSecretMissing") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestDeleteDotenvEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.env")
	if err := WriteDotenvEntry(path, "X", "1"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := DeleteDotenvEntry(path, "X"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := DeleteDotenvEntry(path, "X"); !errors.Is(err, ErrSecretMissing) {
		t.Fatalf("delete-of-missing: err = %v, want ErrSecretMissing", err)
	}
}

func TestMasker_RegisterAndMask(t *testing.T) {
	m := NewMasker()
	m.Register("supersecret")
	m.Register("another")
	m.Register("") // ignored
	got := m.Mask("token=supersecret other=another visible")
	if got != "token=*** other=*** visible" {
		t.Fatalf("Mask = %q", got)
	}
	if vs := m.Values(); len(vs) != 2 {
		t.Fatalf("Values len = %d, want 2", len(vs))
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

// : Cached only registers values with the masker when masked
// is true. Plain config values must render in run output without
// redaction so operators can see what was actually configured.
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
