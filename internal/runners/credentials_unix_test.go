//go:build !windows

package runners

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialRefusesWritableTargetsAndAcceptsProjectedLinks(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "agent-token")
	const raw = "private-projected-credential"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o662); err != nil {
		t.Fatal(err)
	}
	if _, err := FileBearer(directory); err == nil || strings.Contains(err.Error(), raw) {
		t.Fatalf("shared machine credential was accepted or exposed: %v", err)
	}
	if err := os.Rename(path, filepath.Join(directory, "projected-token")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("projected-token", path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(directory, "projected-token"), 0o640); err != nil {
		t.Fatal(err)
	}
	service, err := FileBearer(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := service(t.Context()); got != raw || err != nil {
		t.Fatalf("service could not read its native projection: %v", err)
	}
}
