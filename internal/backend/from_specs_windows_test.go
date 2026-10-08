//go:build windows

package backend_test

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
)

func TestSQLiteSpecPreservesNativeAbsolutePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space and # mark", "state.db")
	spec := (&url.URL{Scheme: "sqlite", Path: "/" + filepath.ToSlash(path)}).String()
	got, err := backend.ParseInlineSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got.Path) || filepath.Clean(got.Path) != filepath.Clean(path) {
		t.Fatalf("sqlite path = %q, want %q", got.Path, path)
	}
	if strings.Contains(got.Path, "%") {
		t.Fatal("sqlite path retained URL escaping")
	}
}
