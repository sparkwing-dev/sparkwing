package bincache_test

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

func TestFailedBinaryPublicationRemovesStaging(t *testing.T) {
	const payload = "complete binary"
	sum := sha256.Sum256([]byte(payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Digest", "sha-256="+base64.StdEncoding.EncodeToString(sum[:]))
		_, err := io.WriteString(w, payload)
		if err != nil {
			return
		}
	}))
	defer server.Close()
	parent := t.TempDir()
	dest := filepath.Join(parent, "binary")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := bincache.TryBinary(t.Context(), server.URL, "", "key", dest); err == nil {
		t.Fatal("published over destination directory")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != "binary" {
		t.Fatalf("staging remains after publication failure: %v %v", entries, err)
	}
}
