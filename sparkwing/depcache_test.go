package sparkwing

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
)

func TestCacheDirRecordsSpecsForTheEngine(t *testing.T) {
	plan := NewPlan()
	n := Job(plan, "test", func(ctx context.Context) error { return nil })

	n.CacheDir(GoModules(), NpmCache(), Dir("service-a/vendor", KeyFromFile("go.sum")))

	got := dirCacheSpecs(n)
	want := []depcache.Spec{
		{Name: "go-modules", Resolver: depcache.ResolverGoModules, KeyFiles: []string{"go.sum"}},
		{Name: "npm", Resolver: depcache.ResolverNpm, KeyFiles: []string{"package-lock.json"}},
		{Name: "vendor", Path: "service-a/vendor", KeyScope: "service-a/vendor", KeyFiles: []string{"go.sum"}},
	}
	if len(got) != len(want) {
		t.Fatalf("specs = %+v, want %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Name != w.Name || g.Path != w.Path || g.Resolver != w.Resolver || g.KeyScope != w.KeyScope ||
			len(g.KeyFiles) != 1 || g.KeyFiles[0] != w.KeyFiles[0] {
			t.Fatalf("spec %d = %+v, want %+v", i, g, w)
		}
	}
	if len(n.DirCaches()) != 3 {
		t.Fatalf("DirCaches len = %d, want 3", len(n.DirCaches()))
	}
}

func TestCacheDirPanicsOnStructuralMisuse(t *testing.T) {
	plan := NewPlan()
	n := Job(plan, "test", func(ctx context.Context) error { return nil })

	assertPanics := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s did not panic", name)
			}
		}()
		fn()
	}
	assertPanics("empty path", func() { n.CacheDir(Dir("", KeyFromFile("x.lock"))) })
	assertPanics("no key file", func() { n.CacheDir(Dir("vendor", KeySource{})) })
}
