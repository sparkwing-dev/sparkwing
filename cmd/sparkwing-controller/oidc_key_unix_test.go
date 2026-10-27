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
// Group read stays open to the process's own group, the Kubernetes fsGroup
// mount; fssecure's tests cover a foreign group.
func TestReadCredentialsRefusesAnOIDCKeyOthersCanRead(t *testing.T) {
	dir := credentialsDir(t, map[string]string{credOIDCKey: string(oidcTestKey(t))})
	for _, mode := range []os.FileMode{0o604, 0o644, 0o660} {
		if err := os.Chmod(filepath.Join(dir, credOIDCKey), mode); err != nil {
			t.Fatal(err)
		}
		if _, err := readCredentials(dir); err == nil || !strings.Contains(err.Error(), credOIDCKey) {
			t.Fatalf("oidc-key at %o = %v, want a refusal naming it", mode, err)
		}
	}
	for _, mode := range []os.FileMode{0o400, 0o440} {
		if err := os.Chmod(filepath.Join(dir, credOIDCKey), mode); err != nil {
			t.Fatal(err)
		}
		if _, err := readCredentials(dir); err != nil {
			t.Fatalf("oidc-key at %o: %v", mode, err)
		}
	}
}
