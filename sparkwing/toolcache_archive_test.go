package sparkwing

import (
	"archive/tar"
	"compress/gzip"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractLintCacheRejectsEscapingEntry(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "cachedir")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	archive := filepath.Join(t.TempDir(), "escape.tar.gz")
	writeRawArchive(t, archive, []*tar.Header{
		{Name: lintCacheManifestName, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(workdir))},
		{Name: "cache/../escape", Typeflag: tar.TypeReg, Mode: 0o644, Size: 5},
	}, map[string][]byte{lintCacheManifestName: []byte(workdir), "cache/../escape": []byte("PWNED")})

	rf, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	if err := extractLintCacheArchive(rf, dest, workdir); err == nil {
		t.Fatal("entry escaping the cache directory was accepted")
	}
	if _, err := os.Stat(filepath.Join(base, "escape")); !os.IsNotExist(err) {
		t.Fatalf("escaping entry was written outside the cache directory (err=%v)", err)
	}
}

func dirPerm(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

func lintArchive(t *testing.T, workdir string, entries []*tar.Header, bodies map[string][]byte) string {
	t.Helper()
	hdrs := append([]*tar.Header{
		{Name: lintCacheManifestName, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(workdir))},
	}, entries...)
	all := map[string][]byte{lintCacheManifestName: []byte(workdir)}
	for k, v := range bodies {
		all[k] = v
	}
	path := filepath.Join(t.TempDir(), "lint.tar.gz")
	writeRawArchive(t, path, hdrs, all)
	return path
}

func TestExtractClampsWideDirectoryModes(t *testing.T) {
	t.Run("lint cache", func(t *testing.T) {
		workdir := t.TempDir()
		dest := filepath.Join(t.TempDir(), "cachedir")
		archive := lintArchive(t, workdir, []*tar.Header{
			{Name: "cache/wide/", Typeflag: tar.TypeDir, Mode: 0o777},
		}, nil)
		rf, err := os.Open(archive)
		if err != nil {
			t.Fatal(err)
		}
		defer rf.Close()
		if err := extractLintCacheArchive(rf, dest, workdir); err != nil {
			t.Fatalf("extract: %v", err)
		}
		if got := dirPerm(t, filepath.Join(dest, "wide")); got != 0o755 {
			t.Fatalf("dir mode = %o, want 755", got)
		}
	})
}

func TestRestoreLintCacheStagedKeepsPreviousCacheOnReject(t *testing.T) {
	workdir := t.TempDir()
	dest := filepath.Join(t.TempDir(), "cachedir")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(dest, "keep.json")
	if err := os.WriteFile(previous, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}

	archive := lintArchive(t, workdir, []*tar.Header{
		{Name: "cache/good", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
		{Name: "cache/../oops", Typeflag: tar.TypeReg, Mode: 0o644, Size: 5},
	}, map[string][]byte{"cache/good": []byte("good"), "cache/../oops": []byte("PWNED")})

	rf, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	if err := extractLintCacheArchiveStaged(rf, dest, workdir); err == nil {
		t.Fatal("escaping entry was accepted")
	}
	got, err := os.ReadFile(previous)
	if err != nil || string(got) != "previous" {
		t.Fatalf("previous cache lost after a rejected restore: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "good")); !os.IsNotExist(err) {
		t.Fatalf("partial extraction leaked into the live cache (err=%v)", err)
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(dest) {
			t.Errorf("staging left %q beside the cache directory", e.Name())
		}
	}
}

func TestRestoreLintCacheStagedReplacesCache(t *testing.T) {
	workdir := t.TempDir()
	dest := filepath.Join(t.TempDir(), "cachedir")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := lintArchive(t, workdir, []*tar.Header{
		{Name: "cache/good", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
	}, map[string][]byte{"cache/good": []byte("good")})

	rf, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	if err := extractLintCacheArchiveStaged(rf, dest, workdir); err != nil {
		t.Fatalf("staged restore: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "good"))
	if err != nil || string(got) != "good" {
		t.Fatalf("restored file = %q err=%v", got, err)
	}
	if perm := dirPerm(t, dest); perm != 0o700 {
		t.Errorf("cache dir mode = %o, want 700", perm)
	}
}

func writeRawArchive(t *testing.T, path string, entries []*tar.Header, bodies map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if b, ok := bodies[h.Name]; ok {
			if _, err := tw.Write(b); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}
