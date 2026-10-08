//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestPathExposureUsesWindowsAccessLists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if mode, exposed := pathExposure(path); mode != "" || !exposed {
		t.Fatalf("inherited access list: mode=%q exposed=%v", mode, exposed)
	}
	if err := fssecure.SecurePrivateConfig(path); err != nil {
		t.Fatal(err)
	}
	if mode, exposed := pathExposure(path); mode != "" || exposed {
		t.Fatalf("protected access list: mode=%q exposed=%v", mode, exposed)
	}
	warning := exposureWarning("config.yaml", path, "600")
	if strings.Contains(warning, "chmod") || !strings.Contains(warning, "Windows Security") {
		t.Fatalf("Windows exposure warning = %q", warning)
	}
}
