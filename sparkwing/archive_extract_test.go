package sparkwing

import (
	"archive/tar"
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
