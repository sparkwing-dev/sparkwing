package git

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCloneUsesUpstreamWithRemovedCacheSettings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CLONE_TEST_ARGS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLONE_TEST_ARGS", log)
	t.Setenv("SPARKWING_GITCACHE_URL", "https://removed.invalid")
	t.Setenv("SPARKWING_CACHE_GRANT", "removed-grant")
	if err := Clone(t.Context(), "https://github.com/example/repo.git", "checkout", WithDepth(2)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "clone\n--depth\n2\n--\nhttps://github.com/example/repo.git\ncheckout\n" {
		t.Fatalf("clone arguments: %s", got)
	}
}
