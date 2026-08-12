package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseAssetsAreAClosedSet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range expectedReleaseAssets() {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateReleaseAssets(dir); err != nil {
		t.Fatal(err)
	}
	first := expectedReleaseAssets()[0]
	if err := os.Rename(filepath.Join(dir, first), filepath.Join(dir, first+"-substitute")); err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseAssets(dir); err == nil {
		t.Fatal("same-count asset substitution passed")
	}
}
