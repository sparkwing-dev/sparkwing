package acceptance_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func testGoSDKDependencyCacheKeys(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for _, dir := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(dir, "lock"), []byte("one"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cache := sparkwing.Dir("deps", sparkwing.KeyFromFile("lock"))
	a, err := cache.Key(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := cache.Key(second)
	if err != nil || a != b {
		t.Fatalf("checkout roots changed key: %q %q %v", a, b, err)
	}
	if err := os.WriteFile(filepath.Join(second, "lock"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err = cache.Key(second)
	if err != nil || a == b {
		t.Fatalf("input change ignored: %q %q %v", a, b, err)
	}
	other, err := sparkwing.Dir("other-deps", sparkwing.KeyFromFile("lock")).Key(first)
	if err != nil || other == a {
		t.Fatal("cache scope not included")
	}
	for _, path := range []string{"../outside", filepath.Join(second, "lock"), "missing"} {
		if _, err := sparkwing.Dir("deps", sparkwing.KeyFromFile(path)).Key(first); err == nil {
			t.Fatalf("unsafe or missing input %q accepted", path)
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(second, "lock"), filepath.Join(first, "outside")); err != nil {
			t.Fatal(err)
		}
		if _, err := sparkwing.Dir("deps", sparkwing.KeyFromFile("outside")).Key(first); err == nil {
			t.Fatal("key read escaped workspace through symlink")
		}
	}
}
