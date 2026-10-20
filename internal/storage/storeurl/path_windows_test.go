//go:build windows

package storeurl

import (
	"path/filepath"
	"testing"
)

func TestWindowsFilesystemURLPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache % space")
	for _, raw := range []string{path, filepath.ToSlash(path), "/" + filepath.ToSlash(path)} {
		got, err := fsPath(raw)
		if err != nil || got != path {
			t.Fatalf("fsPath(%q) = %q, %v; want %q", raw, got, err, path)
		}
	}
	if _, err := fsPath(`C:relative`); err == nil {
		t.Fatal("accepted drive-relative path")
	}
}
