package jobs

import (
	"os"
	"path/filepath"
	"testing"
)

// changelogPairRepo lays out the two files the release rewrite has to keep in
// step, seeded with different content so a test cannot pass by them starting
// equal.
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

// TestWriteChangelogPairLeavesBothCopiesIdentical asserts here what the guard
// test in pkg/docs asserts about the committed tree, against what the rewrite
// produces rather than against what someone remembered to sync afterwards.
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

// The embedded copy must be rewritten, not merely left alone when it happens to
// be absent or stale. Seeding it with different content is what makes this
// meaningful.
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

// A missing embedded copy is an error rather than a silent skip: the guard test
// reads that path and a release that cannot write it must say so.
func TestWriteChangelogPairFailsWhenTheEmbeddedDirIsMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte("root\n"), 0o644); err != nil {
		t.Fatalf("seed root: %v", err)
	}

	if err := writeChangelogPair(dir, "rolled\n"); err == nil {
		t.Fatal("writeChangelogPair returned nil with no pkg/docs directory; a release that cannot sync the embedded copy must fail loudly")
	}
}
