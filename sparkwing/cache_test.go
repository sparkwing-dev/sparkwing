package sparkwing_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestCacheOptions_EmptyIsNoop(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{})
	if n.CacheOpts().HasNamespace() {
		t.Fatalf("empty Cache options should not register coordination")
	}
}

// A CacheOptions with Max / OnLimit / ContentHash / CacheTTL /
// CancelTimeout set but Namespace empty is almost certainly a typo
// (the author meant to enable coordination but forgot the namespace).
// Reject at Plan time so the silent-no-op footgun fails loud.
func TestCacheOptions_NodeRejectsTypoShape_MaxWithoutNamespace(t *testing.T) {
	plan := sparkwing.NewPlan()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on Max without Namespace")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "Max") || !strings.Contains(msg, "Namespace") {
			t.Fatalf("panic should name Max and Namespace, got %q", msg)
		}
	}()
	sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{Max: 3})
}

func TestCacheOptions_PlanRejectsTypoShape_CacheTTLWithoutNamespace(t *testing.T) {
	plan := sparkwing.NewPlan()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on CacheTTL without Namespace")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "CacheTTL") || !strings.Contains(msg, "Namespace") {
			t.Fatalf("panic should name CacheTTL and Namespace, got %q", msg)
		}
	}()
	plan.Cache(sparkwing.CacheOptions{CacheTTL: time.Hour})
}

func TestCacheOptions_NamespaceOnlyApplyDefaults(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{Namespace: "foo"})
	got := n.CacheOpts()
	if got.Namespace != "foo" {
		t.Fatalf("Namespace = %q, want foo", got.Namespace)
	}
	if got.Max != 1 {
		t.Fatalf("Max = %d, want 1", got.Max)
	}
	if got.OnLimit != sparkwing.Queue {
		t.Fatalf("OnLimit = %q, want %q", got.OnLimit, sparkwing.Queue)
	}
	if got.CacheTTL != 0 {
		t.Fatalf("CacheTTL = %s, want 0 (no memoization without ContentHash)", got.CacheTTL)
	}
}

func TestCacheOptions_ContentHashDefaultsTTL(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{
		Namespace:   "foo",
		ContentHash: func(_ context.Context) sparkwing.CacheKey { return "k" },
	})
	if got := n.CacheOpts().CacheTTL; got != sparkwing.DefaultCacheTTL {
		t.Fatalf("CacheTTL = %s, want DefaultCacheTTL", got)
	}
}

func TestCacheOptions_ClampsLongTTL(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{
		Namespace:   "foo",
		ContentHash: func(_ context.Context) sparkwing.CacheKey { return "k" },
		CacheTTL:    365 * 24 * time.Hour,
	})
	if got := n.CacheOpts().CacheTTL; got != sparkwing.MaxCacheTTL {
		t.Fatalf("CacheTTL = %s, want MaxCacheTTL", got)
	}
}

func TestCacheOptions_PanicsOnNegativeMax(t *testing.T) {
	plan := sparkwing.NewPlan()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on Max<0")
		}
	}()
	sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{Namespace: "k", Max: -1})
}

func TestCacheOptions_PanicsOnNegativeTTL(t *testing.T) {
	plan := sparkwing.NewPlan()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on CacheTTL<0")
		}
	}()
	sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{Namespace: "k", CacheTTL: -time.Second})
}

func TestCacheOptions_PlanLevelRejectsCoalesce(t *testing.T) {
	plan := sparkwing.NewPlan()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on plan-level Coalesce")
		}
	}()
	plan.Cache(sparkwing.CacheOptions{Namespace: "k", OnLimit: sparkwing.Coalesce})
}

func TestCacheOptions_PlanLevelRejectsContentHash(t *testing.T) {
	plan := sparkwing.NewPlan()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on plan-level ContentHash")
		}
	}()
	plan.Cache(sparkwing.CacheOptions{
		Namespace:   "k",
		ContentHash: func(_ context.Context) sparkwing.CacheKey { return "v" },
	})
}

func TestCacheOptions_NodeLevelCoalesceOK(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{
		Namespace: "k",
		OnLimit:   sparkwing.Coalesce,
	})
	if n.CacheOpts().OnLimit != sparkwing.Coalesce {
		t.Fatalf("node-level Coalesce should be allowed")
	}
}

func TestCacheOptions_PlanLevelQueueOK(t *testing.T) {
	plan := sparkwing.NewPlan()
	plan.Cache(sparkwing.CacheOptions{Namespace: "prod-deploys", Max: 3})
	got := plan.CacheOpts()
	if got.Namespace != "prod-deploys" || got.Max != 3 {
		t.Fatalf("plan-level cache = %+v", got)
	}
}

func TestNoCache_IsDistinctFromZero(t *testing.T) {
	if sparkwing.NoCache == "" {
		t.Fatal("NoCache must be distinct from the zero CacheKey")
	}
	if !sparkwing.NoCache.IsNoCache() {
		t.Fatal("NoCache.IsNoCache() must report true")
	}
	if sparkwing.CacheKey("").IsNoCache() {
		t.Fatal("zero CacheKey.IsNoCache() must report false")
	}
	if sparkwing.Key("anything").IsNoCache() {
		t.Fatal("a hash-derived CacheKey should not be the NoCache sentinel")
	}
}
