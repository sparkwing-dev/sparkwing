package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPreparedBoundsGzipTail(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := gz.Write(make([]byte, 2<<20)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "download"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractPrepared(dir); err == nil {
		t.Fatal("accepted an oversized gzip tail after tar EOF")
	}
}
