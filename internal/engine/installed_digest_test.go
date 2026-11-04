package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExecutableDigestRehashesAReplacedOrRewrittenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sparkwing")
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want := func(body string) string {
		sum := sha256.Sum256([]byte(body))
		return hex.EncodeToString(sum[:])
	}
	check := func(body string) {
		t.Helper()
		got, err := executableDigest(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if got != want(body) {
			t.Fatalf("digest = %s, want the digest of %q", got, body)
		}
	}
	write(path, "one")
	check("one")
	check("one")

	next := filepath.Join(dir, "next")
	write(next, "two")
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	check("two")

	write(path, "six")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	check("six")
}
