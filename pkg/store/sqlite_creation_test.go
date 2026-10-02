package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreparingExistingSQLitePreservesItsContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	original := []byte("existing database")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := preparePrivateSQLite(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("existing database replaced")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("contents = %q, want %q", got, original)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("creation left temporary files: %v", entries)
	}
}
