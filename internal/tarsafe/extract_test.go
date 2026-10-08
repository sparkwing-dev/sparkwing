package tarsafe

import (
	"archive/tar"
	"bytes"
	"path/filepath"
	"testing"
)

func TestSecureArchiveRelRejectsEscape(t *testing.T) {
	if _, err := secureArchiveRel("../evil"); err == nil {
		t.Fatal("path escaping via .. was not rejected")
	}
	if _, err := secureArchiveRel("/abs/evil"); err == nil {
		t.Fatal("absolute path was not rejected")
	}
	if _, err := secureArchiveRel("ok/nested"); err != nil {
		t.Fatalf("legitimate nested path rejected: %v", err)
	}
}

func rawTar(t *testing.T, entries []*tar.Header, bodies map[string][]byte) *tar.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
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
	return tar.NewReader(&buf)
}

func TestExtractBoundsDecompression(t *testing.T) {
	orig := maxExtractBytes
	maxExtractBytes = 1 << 10
	defer func() { maxExtractBytes = orig }()

	big := make([]byte, 64<<10)
	for name, policy := range map[string]Policy{
		"plain": {AllowSymlinks: true},
		"renamed": {MinDirPerm: 0o700, MinFilePerm: 0o600, Rename: func(n string) (string, bool) {
			return filepath.Base(n), true
		}},
	} {
		t.Run(name, func(t *testing.T) {
			tr := rawTar(t, []*tar.Header{
				{Name: "cache/big", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(big))},
			}, map[string][]byte{"cache/big": big})
			if err := ExtractInRoot(tr, t.TempDir(), policy); err == nil {
				t.Fatal("oversized archive extracted past the cap")
			}
		})
	}
}
