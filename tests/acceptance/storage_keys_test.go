package acceptance_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type keyProbe struct{ keys []string }

func (b *keyProbe) Label() string { return "probe" }
func (b *keyProbe) Exists(_ context.Context, key string) (bool, error) {
	b.keys = append(b.keys, key)
	return false, nil
}
func (*keyProbe) Fetch(context.Context, string, string) (int64, error) {
	return 0, fmt.Errorf("unexpected fetch")
}
func (*keyProbe) Store(context.Context, string, string) (int64, error) {
	return 0, fmt.Errorf("unexpected store")
}

func checkStorageDependencyKeys(t *testing.T) {
	err := quick.Check(func(body []byte, suffix uint32) bool {
		root := t.TempDir()
		path := fmt.Sprintf("deps-%x", suffix)
		var previous string
		for _, checkout := range []string{"first", "relocated"} {
			workspace := filepath.Join(root, checkout)
			if err := os.Mkdir(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "lock"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			want, err := sparkwing.Dir(path, sparkwing.KeyFromFile("lock")).Key(workspace)
			if err != nil {
				t.Fatal(err)
			}
			backend := &keyProbe{}
			ctx := depcache.WithBackend(t.Context(), backend)
			depcache.Restore(ctx, depcache.Spec{Name: path, Path: path, KeyScope: path, KeyFiles: []string{"missing", "lock"}}, "build", workspace)
			if len(backend.keys) != 1 || backend.keys[0] != want || (previous != "" && previous != want) {
				return false
			}
			previous = want
		}
		return true
	}, &quick.Config{MaxCount: 64})
	if err != nil {
		t.Fatal(err)
	}
}

func checkStorageKeyContainment(t *testing.T) {
	err := quick.Check(func(body []byte, suffix uint32) bool {
		root := t.TempDir()
		workspace := filepath.Join(root, "workspace")
		if err := os.Mkdir(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("outside-%x", suffix)
		outside := filepath.Join(root, name)
		if err := os.WriteFile(outside, body, 0o600); err != nil {
			t.Fatal(err)
		}
		files := []string{"../" + name, outside}
		if runtime.GOOS != "windows" {
			if err := os.Symlink(outside, filepath.Join(workspace, "lock")); err != nil {
				t.Fatal(err)
			}
			files = append(files, "lock")
		}
		for _, file := range files {
			backend := &keyProbe{}
			ctx := depcache.WithBackend(t.Context(), backend)
			depcache.Restore(ctx, depcache.Spec{Name: "deps", Path: "deps", KeyFiles: []string{file}}, "build", workspace)
			if len(backend.keys) != 0 {
				return false
			}
		}
		return true
	}, &quick.Config{MaxCount: 64})
	if err != nil {
		t.Fatal(err)
	}
}

func checkStorageGoModulesResolver(t *testing.T) {
	want := filepath.Join(t.TempDir(), "modules")
	t.Setenv("GOMODCACHE", want)
	got, err := (depcache.Spec{Resolver: "go-modules"}).TargetDir(t.TempDir())
	if err != nil || got != want {
		t.Fatalf("GoModules target = %q, %v; want %q", got, err, want)
	}
}
