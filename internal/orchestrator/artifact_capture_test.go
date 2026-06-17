package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	fsstore "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

func newTestArtifactStore(t *testing.T) storage.ArtifactStore {
	t.Helper()
	st, err := fsstore.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	return st
}

func writeArtifactFile(t *testing.T, root, rel string, data []byte, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, mode); err != nil {
		t.Fatal(err)
	}
}

func readManifest(t *testing.T, store storage.ArtifactStore, digest string) artifactManifest {
	t.Helper()
	rc, err := store.Get(context.Background(), artifactManifestKey(digest))
	if err != nil {
		t.Fatalf("get manifest %s: %v", digest, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	var m artifactManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	return m
}

func TestCaptureArtifacts_StoresFilesAndManifest(t *testing.T) {
	ws := t.TempDir()
	store := newTestArtifactStore(t)
	writeArtifactFile(t, ws, "dist/a.txt", []byte("alpha"), 0o644)
	writeArtifactFile(t, ws, "dist/sub/b.txt", []byte("bravo"), 0o755)
	writeArtifactFile(t, ws, "ignored/c.txt", []byte("charlie"), 0o644)

	digest, err := captureArtifacts(context.Background(), store, ws, []string{"dist/**"})
	if err != nil {
		t.Fatalf("captureArtifacts: %v", err)
	}
	m := readManifest(t, store, digest)
	if len(m.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(m.Entries), m.Entries)
	}
	if m.Entries[0].Path != "dist/a.txt" || m.Entries[1].Path != "dist/sub/b.txt" {
		t.Fatalf("entries not sorted by path: %+v", m.Entries)
	}
	if m.Entries[1].Mode != 0o755 {
		t.Fatalf("mode bits not preserved: got %o want 0755", m.Entries[1].Mode)
	}
	rc, err := store.Get(context.Background(), artifactBlobKey(m.Entries[0].Digest))
	if err != nil {
		t.Fatalf("get blob: %v", err)
	}
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(b) != "alpha" {
		t.Fatalf("blob content: got %q want alpha", b)
	}
}

func TestCaptureArtifacts_EmptyMatchYieldsEmptyManifest(t *testing.T) {
	ws := t.TempDir()
	store := newTestArtifactStore(t)
	writeArtifactFile(t, ws, "other.txt", []byte("x"), 0o644)

	digest, err := captureArtifacts(context.Background(), store, ws, []string{"dist/**"})
	if err != nil {
		t.Fatalf("captureArtifacts: %v", err)
	}
	if digest == "" {
		t.Fatal("empty match should still produce a manifest digest")
	}
	if m := readManifest(t, store, digest); len(m.Entries) != 0 {
		t.Fatalf("want empty manifest, got %+v", m.Entries)
	}
}

func TestCaptureArtifacts_DirectoryNameExpandsToFiles(t *testing.T) {
	ws := t.TempDir()
	store := newTestArtifactStore(t)
	writeArtifactFile(t, ws, "dist/a.txt", []byte("a"), 0o644)
	writeArtifactFile(t, ws, "dist/b.txt", []byte("b"), 0o644)

	digest, err := captureArtifacts(context.Background(), store, ws, []string{"dist"})
	if err != nil {
		t.Fatalf("captureArtifacts: %v", err)
	}
	if m := readManifest(t, store, digest); len(m.Entries) != 2 {
		t.Fatalf("naming a directory should capture its files; got %+v", m.Entries)
	}
}

func TestCaptureArtifacts_DedupsIdenticalContent(t *testing.T) {
	ws := t.TempDir()
	store := newTestArtifactStore(t)
	writeArtifactFile(t, ws, "a.txt", []byte("same"), 0o644)
	writeArtifactFile(t, ws, "b.txt", []byte("same"), 0o644)

	digest, err := captureArtifacts(context.Background(), store, ws, []string{"*.txt"})
	if err != nil {
		t.Fatalf("captureArtifacts: %v", err)
	}
	m := readManifest(t, store, digest)
	if len(m.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(m.Entries))
	}
	if m.Entries[0].Digest != m.Entries[1].Digest {
		t.Fatalf("identical content should share a blob digest: %+v", m.Entries)
	}
}

func TestCaptureArtifacts_FollowsSymlinkToContent(t *testing.T) {
	ws := t.TempDir()
	store := newTestArtifactStore(t)
	writeArtifactFile(t, ws, "real.txt", []byte("payload"), 0o644)
	if err := os.Symlink(filepath.Join(ws, "real.txt"), filepath.Join(ws, "link.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	digest, err := captureArtifacts(context.Background(), store, ws, []string{"link.txt"})
	if err != nil {
		t.Fatalf("captureArtifacts: %v", err)
	}
	m := readManifest(t, store, digest)
	if len(m.Entries) != 1 {
		t.Fatalf("want 1 entry, got %+v", m.Entries)
	}
	rc, err := store.Get(context.Background(), artifactBlobKey(m.Entries[0].Digest))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(b) != "payload" {
		t.Fatalf("symlink not followed to content: got %q", b)
	}
}

func TestCaptureArtifacts_BrokenSymlinkInDeclaredPathErrors(t *testing.T) {
	ws := t.TempDir()
	store := newTestArtifactStore(t)
	if err := os.Symlink(filepath.Join(ws, "missing.txt"), filepath.Join(ws, "dangling.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := captureArtifacts(context.Background(), store, ws, []string{"dangling.txt"}); err == nil {
		t.Fatal("broken symlink in a declared path should error")
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"dist/**", "dist/a.txt", true},
		{"dist/**", "dist/sub/b.txt", true},
		{"dist/**", "dist", true},
		{"dist/**", "other/a.txt", false},
		{"**", "a/b/c.txt", true},
		{"a/**/c.txt", "a/c.txt", true},
		{"a/**/c.txt", "a/x/y/c.txt", true},
		{"*.json", "cover.json", true},
		{"*.json", "sub/cover.json", false},
		{"coverage/shard-1.json", "coverage/shard-1.json", true},
		{"dir/*", "dir/file", true},
		{"dir/*", "dir/sub/file", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.name); got != c.want {
			t.Errorf("globMatch(%q,%q)=%v want %v", c.pattern, c.name, got, c.want)
		}
	}
}
