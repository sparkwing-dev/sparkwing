package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	fsstore "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
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

func TestCaptureArtifacts_SymlinkOutOfWorkspace(t *testing.T) {
	cases := []struct {
		name      string
		globs     []string
		setup     func(t *testing.T, ws, outside string)
		wantErr   bool
		wantPaths []string
	}{
		{
			name:  "literal file link is refused",
			globs: []string{"report.txt"},
			setup: func(t *testing.T, ws, outside string) {
				symlinkOrSkip(t, filepath.Join(outside, "secret.txt"), filepath.Join(ws, "report.txt"))
			},
			wantErr: true,
		},
		{
			name:  "directory link is skipped",
			globs: []string{"dist/**"},
			setup: func(t *testing.T, ws, outside string) {
				symlinkOrSkip(t, outside, filepath.Join(ws, "dist"))
			},
		},
		{
			name:  "literal directory link is skipped",
			globs: []string{"dist"},
			setup: func(t *testing.T, ws, outside string) {
				symlinkOrSkip(t, outside, filepath.Join(ws, "dist"))
			},
		},
		{
			name:  "wildcard file link is skipped",
			globs: []string{"*"},
			setup: func(t *testing.T, ws, outside string) {
				writeArtifactFile(t, ws, "dist/a.txt", []byte("alpha"), 0o644)
				symlinkOrSkip(t, filepath.Join(outside, "secret.txt"), filepath.Join(ws, "report.txt"))
			},
			wantPaths: []string{"dist/a.txt"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := t.TempDir()
			outside := t.TempDir()
			writeArtifactFile(t, outside, "secret.txt", []byte("credential"), 0o600)
			store := newTestArtifactStore(t)
			c.setup(t, ws, outside)

			log := &artifactWarnLogger{}
			ctx := sparkwingruntime.WithLogger(context.Background(), log)
			digest, err := captureArtifacts(ctx, store, ws, c.globs)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want refusal, got manifest %+v", readManifest(t, store, digest))
				}
				if !errors.Is(err, errArtifactOutsideWorkspace) {
					t.Fatalf("want escape refusal, got %v", err)
				}
				if strings.Contains(err.Error(), outside) {
					t.Fatalf("failure text names an outside path: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("captureArtifacts: %v", err)
			}
			m := readManifest(t, store, digest)
			var paths []string
			for _, e := range m.Entries {
				paths = append(paths, e.Path)
			}
			if !slices.Equal(paths, c.wantPaths) {
				t.Fatalf("paths = %v, want %v", paths, c.wantPaths)
			}
			if !log.sawWarning() {
				t.Fatal("skipping an out-of-workspace match should warn")
			}
		})
	}
}

func TestCaptureArtifacts_UnresolvableMatchHidesOutsidePath(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	store := newTestArtifactStore(t)
	symlinkOrSkip(t, filepath.Join(outside, "missing", "secret.txt"), filepath.Join(ws, "report.txt"))

	_, err := captureArtifacts(context.Background(), store, ws, []string{"report.txt"})
	if err == nil {
		t.Fatal("an unresolvable declared path should error")
	}
	if strings.Contains(err.Error(), outside) {
		t.Fatalf("failure text names an outside path: %v", err)
	}
	if !errors.Is(err, errArtifactUnresolvable) {
		t.Fatalf("want unresolvable sentinel, got %v", err)
	}
}

func TestCaptureArtifacts_UploadsTheBytesItHashed(t *testing.T) {
	ws := t.TempDir()
	writeArtifactFile(t, ws, "a.txt", []byte("first"), 0o644)
	replaced := false
	store := &replaceOnLookupStore{
		ArtifactStore: newTestArtifactStore(t),
		before: func() {
			if replaced {
				return
			}
			replaced = true
			swap := filepath.Join(t.TempDir(), "swap.txt")
			if err := os.WriteFile(swap, []byte("second"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(swap, filepath.Join(ws, "a.txt")); err != nil {
				t.Fatal(err)
			}
		},
	}

	digest, err := captureArtifacts(context.Background(), store, ws, []string{"a.txt"})
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
	if string(b) != "first" {
		t.Fatalf("blob content %q does not match the digest it is stored under", b)
	}
}

type replaceOnLookupStore struct {
	storage.ArtifactStore
	before func()
}

func (s *replaceOnLookupStore) Has(ctx context.Context, key string) (bool, error) {
	if strings.HasPrefix(key, "artifacts/blobs/") {
		s.before()
	}
	return s.ArtifactStore.Has(ctx, key)
}

type artifactWarnLogger struct {
	mu    sync.Mutex
	warns int
}

func (l *artifactWarnLogger) Log(level, msg string) {
	l.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (l *artifactWarnLogger) Emit(rec sparkwing.LogRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rec.Level == "warn" {
		l.warns++
	}
}

func (l *artifactWarnLogger) sawWarning() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.warns > 0
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
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
