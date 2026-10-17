package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestSecretInputRefusesEscapingSymlink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "secret")
	if err := fssecure.WriteFile(target, []byte("outside-secret")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	inv, err := clisurface.Parse("sparkwing", []string{"secrets", "set", "--secret", "TOKEN", "--file", path})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := readSecretValue(inv, nil); err == nil || got != "" {
		t.Fatalf("escaped secret input = %q, %v", got, err)
	}
}
