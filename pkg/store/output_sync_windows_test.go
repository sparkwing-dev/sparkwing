//go:build windows

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsOutputDurableCreateReplaceAndResync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "output.json")
	for _, body := range []string{`{"first":1}`, `{"second":2}`, `{"second":2}`, ""} {
		if err := WriteOutputFileDurably(path, []byte(body)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != body {
			t.Fatalf("output = %q, %v; want %q", got, err, body)
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
			t.Fatalf("publication left temporary files: %v, %v", entries, err)
		}
	}
}
