//go:build !windows

package runners

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMachineCredentialRefusesSharedFilesAndProjectedLinks(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "agent-token")
	const raw = "private-projected-credential"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := FileBearer(directory, true); err == nil || strings.Contains(err.Error(), raw) {
		t.Fatalf("shared machine credential was accepted or exposed: %v", err)
	}
	if err := os.Rename(path, filepath.Join(directory, "projected-token")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("projected-token", path); err != nil {
		t.Fatal(err)
	}
	if _, err := FileBearer(directory, true); err == nil {
		t.Fatal("machine startup followed a credential link")
	}
	service, err := FileBearer(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := service(t.Context()); got != raw || err != nil {
		t.Fatalf("service could not read its native projection: %v", err)
	}
}
