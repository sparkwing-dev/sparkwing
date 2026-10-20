package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Transfer URLs a runner holds survive a controller restart.
func TestDataSigningKeyIsPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data-signing.key")
	first, err := dataSigningKey(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := dataSigningKey(path)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatalf("restart key differs: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file = %v, %v; want mode 0600", info, err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dataSigningKey(path); err == nil {
		t.Fatal("a truncated signing key was accepted")
	}
}
