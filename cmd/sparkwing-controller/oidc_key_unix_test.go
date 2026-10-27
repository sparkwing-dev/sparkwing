//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The OIDC signing key mints tokens cloud providers trust, so a copy other
// local users can read is refused at startup, as private keys are elsewhere.
func TestReadCredentialsRefusesAGroupReadableOIDCKey(t *testing.T) {
	dir := credentialsDir(t, map[string]string{credOIDCKey: string(oidcTestKey(t))})
	for _, mode := range []os.FileMode{0o640, 0o604} {
		if err := os.Chmod(filepath.Join(dir, credOIDCKey), mode); err != nil {
			t.Fatal(err)
		}
		if _, err := readCredentials(dir); err == nil || !strings.Contains(err.Error(), credOIDCKey) {
			t.Fatalf("oidc-key at %o = %v, want a refusal naming it", mode, err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, credOIDCKey), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentials(dir); err != nil {
		t.Fatalf("owner-only oidc-key: %v", err)
	}
}
