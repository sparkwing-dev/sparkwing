package jobs

import (
	"os"
	"path/filepath"
	"testing"
)

func changelogPairRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte("root before\n"), 0o644); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "docs", "changelog.md"), []byte("embedded before\n"), 0o644); err != nil {
		t.Fatalf("seed embedded: %v", err)
	}
	return dir
}

func TestWriteChangelogPairLeavesBothCopiesIdentical(t *testing.T) {
	dir := changelogPairRepo(t)
	rolled := "# Changelog\n\n## [Unreleased]\n\n## [v9.9.9] - 2026-01-01\n\n- a thing\n"

	if err := writeChangelogPair(dir, rolled); err != nil {
		t.Fatalf("writeChangelogPair: %v", err)
	}

	root, err := os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	embedded, err := os.ReadFile(filepath.Join(dir, "pkg", "docs", "changelog.md"))
	if err != nil {
		t.Fatalf("read embedded: %v", err)
	}

	if string(root) != rolled {
		t.Errorf("root = %q, want the rolled body", root)
	}
	if string(root) != string(embedded) {
		t.Errorf("copies diverged\n root     = %q\n embedded = %q", root, embedded)
	}
}

func TestWriteChangelogPairOverwritesAStaleEmbeddedCopy(t *testing.T) {
	dir := changelogPairRepo(t)

	before, err := os.ReadFile(filepath.Join(dir, "pkg", "docs", "changelog.md"))
	if err != nil {
		t.Fatalf("read embedded: %v", err)
	}
	if string(before) != "embedded before\n" {
		t.Fatalf("fixture is not stale to begin with: %q", before)
	}

	if err := writeChangelogPair(dir, "rolled\n"); err != nil {
		t.Fatalf("writeChangelogPair: %v", err)
	}

	after, err := os.ReadFile(filepath.Join(dir, "pkg", "docs", "changelog.md"))
	if err != nil {
		t.Fatalf("read embedded: %v", err)
	}
	if string(after) != "rolled\n" {
		t.Errorf("embedded = %q, want the rolled body; a stale copy was left in place", after)
	}
}

func TestWriteChangelogPairFailsWhenTheEmbeddedDirIsMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte("root\n"), 0o644); err != nil {
		t.Fatalf("seed root: %v", err)
	}

	if err := writeChangelogPair(dir, "rolled\n"); err == nil {
		t.Fatal("writeChangelogPair returned nil with no pkg/docs directory; a release that cannot sync the embedded copy must fail loudly")
	}
}
