package sparkwing

import (
	"context"
	"testing"
)

func TestCacheDeclarationsReachPlanDocument(t *testing.T) {
	plan := NewPlan()
	Job(plan, "test", func(context.Context) error { return nil }).Cache(
		GoModules(), Npm(), Dir("service-a/vendor", KeyFromFile("go.sum")),
	)
	jobs := planDocument(plan).Jobs
	if len(jobs) != 1 || len(jobs[0].Caches) != 3 {
		t.Fatalf("cache declarations = %+v", jobs)
	}
	if jobs[0].Caches[0].Name != "go-modules" || jobs[0].Caches[1].Name != "npm" || jobs[0].Caches[2].Path != "service-a/vendor" {
		t.Fatalf("cache declarations = %+v", jobs[0].Caches)
	}
}

func TestCacheRejectsIncompleteDeclaration(t *testing.T) {
	plan := NewPlan()
	job := Job(plan, "test", func(context.Context) error { return nil })
	for name, cache := range map[string]DirCache{
		"empty path":  Dir("", KeyFromFile("x.lock")),
		"no key file": Dir("vendor", KeySource{}),
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("incomplete cache accepted")
				}
			}()
			job.Cache(cache)
		})
	}
}
