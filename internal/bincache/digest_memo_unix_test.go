//go:build !windows

package bincache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDigestMemoReusesUnchangedFilesAndRehashesRewrites(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	enabled, after := digestMemoEnabled, stableAfter
	digestMemoEnabled, stableAfter = true, 0
	t.Cleanup(func() { digestMemoEnabled, stableAfter = enabled, after })
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	writeFile(t, file, "package main\n")
	hash := func() string {
		t.Helper()
		var out bytes.Buffer
		if _, err := hashFilesInto(&out, dir, []string{file}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	first := hash()
	memo := openDigestMemo(dir)
	entry, ok := memo.known["main.go"]
	if !ok {
		t.Fatalf("no digest recorded for main.go in %s", memo.path)
	}
	identity, _, _ := strings.Cut(entry, " ")
	planted := sha256.Sum256([]byte("planted"))
	if err := os.WriteFile(memo.path, []byte("main.go\x00"+identity+" "+hex.EncodeToString(planted[:])+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := hash(); !strings.Contains(got, hex.EncodeToString(planted[:])) {
		t.Fatalf("unchanged file was read again instead of reusing its record: %q", got)
	}
	writeFile(t, file, "package main\n\nfunc main() {}\n")
	if got := hash(); got == first || strings.Contains(got, hex.EncodeToString(planted[:])) {
		t.Fatalf("rewritten file reused a stale digest: %q", got)
	}
}

func TestDigestMemoIsOffForTestBinaries(t *testing.T) {
	if digestMemoEnabled {
		t.Fatal("a go test binary would write digest records into the real home")
	}
}
