package depcache_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
)

type remoteReader struct {
	found           bool
	err             error
	probes, fetches int
}

func (r *remoteReader) Exists(context.Context, string) (bool, error) {
	r.probes++
	return r.found, r.err
}

func (r *remoteReader) Fetch(_ context.Context, _, dir string) (int64, error) {
	r.fetches++
	if r.err != nil {
		return 0, r.err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	return 6, os.WriteFile(filepath.Join(dir, "tool"), []byte("remote"), 0o755)
}

func cacheWorkspace(t *testing.T) (string, depcache.Spec) {
	t.Helper()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "lock"), []byte("locked"), 0o644); err != nil {
		t.Fatal(err)
	}
	return work, depcache.Spec{Name: "tools", Path: "tools", KeyFiles: []string{"lock"}}
}

func TestRemoteReaderKeepsSavesLocal(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	remote := &remoteReader{}
	ctx := depcache.WithRemoteReader(t.Context(), remote)
	work, spec := cacheWorkspace(t)
	run := depcache.Restore(ctx, spec, "job", work)
	if remote.probes != 1 || remote.fetches != 0 {
		t.Fatalf("remote miss = %+v", remote)
	}
	if err := os.MkdirAll(filepath.Join(work, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "tools", "tool"), []byte("local"), 0o755); err != nil {
		t.Fatal(err)
	}
	run.Save(ctx, nil)
	if remote.probes != 1 || remote.fetches != 0 {
		t.Fatal("save contacted remote cache")
	}
	remote.found = true
	next, _ := cacheWorkspace(t)
	depcache.Restore(ctx, spec, "job", next)
	if body, err := os.ReadFile(filepath.Join(next, "tools", "tool")); err != nil || string(body) != "local" {
		t.Fatalf("local restore = %q, %v", body, err)
	}
	if remote.probes != 1 || remote.fetches != 0 {
		t.Fatal("local hit queried remote cache")
	}
}

func TestRemoteReaderRestoresAndFailsCold(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "hit", true: "unavailable"}[failed], func(t *testing.T) {
			t.Setenv("SPARKWING_HOME", t.TempDir())
			r := &remoteReader{found: true}
			if failed {
				r.err = errors.New("remote unavailable")
			}
			ctx := depcache.WithRemoteReader(t.Context(), r)
			work, spec := cacheWorkspace(t)
			depcache.Restore(ctx, spec, "job", work)
			body, err := os.ReadFile(filepath.Join(work, "tools", "tool"))
			if failed {
				if !os.IsNotExist(err) {
					t.Fatalf("failed remote restore published %q: %v", body, err)
				}
			} else if err != nil || string(body) != "remote" {
				t.Fatalf("remote restore = %q, %v", body, err)
			}
		})
	}
}
