//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirectHomeWritersCreatePrivatePaths(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDashboardPaths(home); err != nil {
		t.Fatal(err)
	}
	assertLocalPermission(t, home, 0o700)

	describe := filepath.Join(home, "cache", "describe", "schema.json")
	writeDescribeFile(describe, []byte(`[]`))
	assertLocalPermission(t, filepath.Dir(describe), 0o700)
	assertLocalPermission(t, describe, 0o600)

	stamp := filepath.Join(home, "last-version.d", "install")
	writeVersionStamp(stamp, "/usr/local/bin/sparkwing", "v0.36.0")
	assertLocalPermission(t, filepath.Dir(stamp), 0o700)
	assertLocalPermission(t, stamp, 0o600)
}

func assertLocalPermission(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
