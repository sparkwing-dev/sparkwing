package sparkwing

import (
	"archive/tar"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractLintCacheBoundsDecompression(t *testing.T) {
	orig := maxExtractBytes
	maxExtractBytes = 1 << 10
	defer func() { maxExtractBytes = orig }()

	workdir := t.TempDir()
	archive := filepath.Join(t.TempDir(), "bomb.tar.gz")
	big := make([]byte, 64<<10)
	writeRawDepArchive(t, archive, []*tar.Header{
		{Name: lintCacheManifestName, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(workdir))},
		{Name: "cache/big", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(big))},
	}, map[string][]byte{lintCacheManifestName: []byte(workdir), "cache/big": big})

	rf, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	if err := extractLintCacheArchive(rf, t.TempDir(), workdir); err == nil {
		t.Fatal("oversized lint cache archive extracted past the cap")
	}
}

func TestExtractLintCacheRejectsEscapingEntry(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "cachedir")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	archive := filepath.Join(t.TempDir(), "escape.tar.gz")
	writeRawDepArchive(t, archive, []*tar.Header{
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
	writeRawDepArchive(t, path, hdrs, all)
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

	t.Run("dep cache", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "depcache")
		archive := filepath.Join(t.TempDir(), "wide.tar.gz")
		writeRawDepArchive(t, archive, []*tar.Header{
			{Name: "wide", Typeflag: tar.TypeDir, Mode: 0o777},
		}, nil)
		rf, err := os.Open(archive)
		if err != nil {
			t.Fatal(err)
		}
		defer rf.Close()
		if err := extractDepCacheArchive(rf, dest); err != nil {
			t.Fatalf("extract: %v", err)
		}
		if got := dirPerm(t, filepath.Join(dest, "wide")); got != 0o755 {
			t.Fatalf("dir mode = %o, want 755", got)
		}
	})
}

func TestExtractCreatesImplicitParentsAtStandardMode(t *testing.T) {
	base := t.TempDir()
	reference := filepath.Join(base, "reference")
	if err := os.Mkdir(reference, 0o755); err != nil {
		t.Fatal(err)
	}
	want := dirPerm(t, reference)

	dest := filepath.Join(base, "depcache")
	archive := filepath.Join(t.TempDir(), "implicit.tar.gz")
	writeRawDepArchive(t, archive, []*tar.Header{
		{Name: "pkg/mod/foo.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2},
	}, map[string][]byte{"pkg/mod/foo.txt": []byte("ok")})

	rf, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	if err := extractDepCacheArchive(rf, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, rel := range []string{"pkg", "pkg/mod"} {
		if got := dirPerm(t, filepath.Join(dest, filepath.FromSlash(rel))); got != want {
			t.Errorf("%s mode = %o, want %o", rel, got, want)
		}
	}
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
