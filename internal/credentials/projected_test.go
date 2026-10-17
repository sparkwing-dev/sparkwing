package credentials_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestProjectedCredential(t *testing.T) {
	directory := t.TempDir()
	version := filepath.Join(directory, "..2026_10_16")
	if err := os.Mkdir(version, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.WriteFile(filepath.Join(version, "agent-token"), []byte("projected-token\n")); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"..data": "..2026_10_16", "agent-token": "..data/agent-token"} {
		if err := os.Symlink(target, filepath.Join(directory, link)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}
	dir, err := credentials.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	got, err := dir.Read("agent-token")
	if err != nil || got != "projected-token" {
		t.Fatalf("projected credential = %q, %v", got, err)
	}
}

func TestCredentialRefusesEscapingSymlink(t *testing.T) {
	directory := t.TempDir()
	outside := filepath.Join(t.TempDir(), "token")
	if err := fssecure.WriteFile(outside, []byte("outside-token")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "agent-token")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	dir, err := credentials.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := dir.Read("agent-token"); err == nil || got != "" {
		t.Fatalf("escaped credential = %q, %v", got, err)
	}
}
