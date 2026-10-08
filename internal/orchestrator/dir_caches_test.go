package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var dirCacheFixture struct {
	mu          sync.Mutex
	dir, lock   string
	beforeSaw   bool
	runSaw      bool
	writeOnRun  bool
	runCalls    int
	beforeCalls int
}

type dirCachePipe struct{ sparkwing.Base }

func (dirCachePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	f := &dirCacheFixture
	f.mu.Lock()
	dir, lock := f.dir, f.lock
	f.mu.Unlock()
	marker := filepath.Join(dir, "restored.txt")
	sparkwing.Job(plan, "deps", func(ctx context.Context) error {
		_, err := os.Stat(marker)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.runCalls++
		f.runSaw = err == nil
		if f.writeOnRun {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(marker, []byte("dep"), 0o644)
		}
		return nil
	}).CacheDir(sparkwing.Dir(dir, sparkwing.KeyFromFile(lock))).
		BeforeRun(func(ctx context.Context) error {
			_, err := os.Stat(marker)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.beforeCalls++
			f.beforeSaw = err == nil
			return nil
		})
	return nil
}

func init() {
	register("dir-cache-host", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &dirCachePipe{} })
}

func TestDirCacheHostRestoresBeforeHooksAndSavesAfterSuccess(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CACHE_URL", "")
	t.Setenv("SPARKWING_GITCACHE_URL", "")
	work := t.TempDir()
	f := &dirCacheFixture
	f.mu.Lock()
	f.dir = filepath.Join(work, "vendor")
	f.lock = filepath.Join(work, "deps.lock")
	f.writeOnRun = true
	f.mu.Unlock()
	if err := os.WriteFile(f.lock, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func() {
		t.Helper()
		res, err := orchestrator.RunLocal(context.Background(), newPathsWithStore(t), orchestrator.Options{Pipeline: "dir-cache-host"})
		if err != nil || res.Status != "success" {
			t.Fatalf("run: status=%v err=%v", res, err)
		}
	}

	run()
	f.mu.Lock()
	if f.runSaw || f.beforeSaw {
		f.mu.Unlock()
		t.Fatal("first run saw a restored directory on an empty cache")
	}
	f.writeOnRun = false
	f.mu.Unlock()

	if err := os.RemoveAll(f.dir); err != nil {
		t.Fatal(err)
	}
	run()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.beforeSaw {
		t.Fatal("BeforeRun did not see the restored directory; the host must restore before hooks")
	}
	if !f.runSaw {
		t.Fatal("second run did not see the directory saved by the first")
	}
}
