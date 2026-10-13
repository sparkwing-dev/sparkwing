package nodehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type cacheFixture struct {
	t    *testing.T
	repo string
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CACHE_URL", "")
	t.Setenv("SPARKWING_GITCACHE_URL", "")
	f := &cacheFixture{t: t, repo: t.TempDir()}
	f.write("go.sum", "example.com/mod v1.0.0\n")
	return f
}

func (f *cacheFixture) write(rel, body string) {
	f.t.Helper()
	p := filepath.Join(f.repo, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *cacheFixture) run(specs []depcache.Spec, install string, outcome sparkwing.Outcome) map[string]string {
	f.t.Helper()
	for _, s := range specs {
		if err := os.RemoveAll(filepath.Join(f.repo, s.Path)); err != nil {
			f.t.Fatal(err)
		}
	}
	found := map[string]string{}
	exec := func(context.Context, *NodeRun) Result {
		for _, s := range specs {
			body, err := os.ReadFile(filepath.Join(f.repo, s.Path, "dep.txt"))
			if err == nil {
				found[s.Path] = string(body)
				continue
			}
			f.write(filepath.Join(s.Path, "dep.txt"), install+" "+s.Path)
		}
		if outcome == sparkwing.Success {
			return Result{Outcome: outcome}
		}
		return Result{Outcome: outcome, Err: errors.New("install broke")}
	}
	cfg := &Config{Session: SessionConfig{Dir: f.repo}}
	n := &NodeRun{Plan: nodeproto.PlanNode{ID: "build", Caches: specs}}
	if res := cachesLayer(cfg, exec)(context.Background(), n); res.Outcome != outcome {
		f.t.Fatalf("outcome %s, want %s", res.Outcome, outcome)
	}
	return found
}

func modules() []depcache.Spec {
	return []depcache.Spec{{Name: "mods", Path: "vendor", KeyFiles: []string{"go.sum"}}}
}

func TestCachesRestoreWhatASuccessSaved(t *testing.T) {
	f := newCacheFixture(t)
	if got := f.run(modules(), "first", sparkwing.Success); len(got) != 0 {
		t.Fatalf("a cold cache restored %v", got)
	}
	got := f.run(modules(), "second", sparkwing.Success)
	if got["vendor"] != "first vendor" {
		t.Fatalf("restored %q, want the first run's install", got["vendor"])
	}
}

func TestCachesMissWhenTheKeyFileChanges(t *testing.T) {
	f := newCacheFixture(t)
	f.run(modules(), "first", sparkwing.Success)
	f.write("go.sum", "example.com/mod v2.0.0\n")
	if got := f.run(modules(), "second", sparkwing.Success); len(got) != 0 {
		t.Fatalf("a new key restored %v", got)
	}
	if got := f.run(modules(), "third", sparkwing.Success); got["vendor"] != "second vendor" {
		t.Fatalf("restored %q, want the install saved under the new key", got["vendor"])
	}
}

func TestCachesNeverSaveAfterAFailure(t *testing.T) {
	for _, outcome := range []sparkwing.Outcome{sparkwing.Failed, sparkwing.Cancelled} {
		t.Run(string(outcome), func(t *testing.T) {
			f := newCacheFixture(t)
			f.run(modules(), "broken", outcome)
			if got := f.run(modules(), "fresh", sparkwing.Success); len(got) != 0 {
				t.Fatalf("a %s run seeded the cache with %v", outcome, got)
			}
			if got := f.run(modules(), "later", sparkwing.Success); got["vendor"] != "fresh vendor" {
				t.Fatalf("restored %q, want the first successful install", got["vendor"])
			}
		})
	}
}

func TestCachesKeyScopeSeparatesDirsSharingAKeyFile(t *testing.T) {
	f := newCacheFixture(t)
	specs := []depcache.Spec{
		{Name: "deps", Path: "a", KeyScope: "a", KeyFiles: []string{"go.sum"}},
		{Name: "deps", Path: "b", KeyScope: "b", KeyFiles: []string{"go.sum"}},
	}
	f.run(specs, "first", sparkwing.Success)
	got := f.run(specs, "second", sparkwing.Success)
	if got["a"] != "first a" || got["b"] != "first b" {
		t.Fatalf("restored %v, want each directory's own install", got)
	}
}
