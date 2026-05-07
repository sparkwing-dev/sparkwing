package sparkwing_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

func TestCacheOptions_EmptyIsNoop(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{})
	if n.CacheOpts().HasKey() {
		t.Fatalf("empty Cache options should not register coordination")
	}
}

// SDK-038: a CacheOptions with Max / OnLimit / CacheKey / CacheTTL /
// CancelTimeout set but Key empty is almost certainly a typo (the
// author meant to enable coordination but forgot the key). Reject
// at Plan time so the silent-no-op footgun fails loud.
func TestCacheOptions_NodeRejectsTypoShape_MaxWithoutKey(t *testing.T) {
	plan := sparkwing.NewPlan()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on Max without Key")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "Max") || !strings.Contains(msg, "Key") {
			t.Fatalf("panic should name Max and Key, got %q", msg)
		}
	}()
	sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{Max: 3})
}

func TestCacheOptions_PlanRejectsTypoShape_CacheTTLWithoutKey(t *testing.T) {
	plan := sparkwing.NewPlan()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on CacheTTL without Key")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "CacheTTL") || !strings.Contains(msg, "Key") {
			t.Fatalf("panic should name CacheTTL and Key, got %q", msg)
		}
	}()
	plan.Cache(sparkwing.CacheOptions{CacheTTL: time.Hour})
}

func TestCacheOptions_KeyOnlyApplyDefaults(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{Key: "foo"})
	got := n.CacheOpts()
	if got.Key != "foo" {
		t.Fatalf("Key = %q, want foo", got.Key)
	}
	if got.Max != 1 {
		t.Fatalf("Max = %d, want 1", got.Max)
	}
	if got.OnLimit != sparkwing.Queue {
		t.Fatalf("OnLimit = %q, want %q", got.OnLimit, sparkwing.Queue)
	}
	if got.CacheTTL != 0 {
		t.Fatalf("CacheTTL = %s, want 0 (no memoization without CacheKey)", got.CacheTTL)
	}
}

func TestCacheOptions_CacheKeyDefaultsTTL(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{
		Key:      "foo",
		CacheKey: func(_ context.Context) sparkwing.CacheKey { return "k" },
	})
	if got := n.CacheOpts().CacheTTL; got != sparkwing.DefaultCacheTTL {
		t.Fatalf("CacheTTL = %s, want DefaultCacheTTL", got)
	}
}

func TestCacheOptions_ClampsLongTTL(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{
		Key:      "foo",
		CacheKey: func(_ context.Context) sparkwing.CacheKey { return "k" },
		CacheTTL: 365 * 24 * time.Hour,
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
	sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{Key: "k", Max: -1})
}

func TestCacheOptions_PanicsOnNegativeTTL(t *testing.T) {
	plan := sparkwing.NewPlan()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on CacheTTL<0")
		}
	}()
	sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{Key: "k", CacheTTL: -time.Second})
}

func TestCacheOptions_PlanLevelRejectsCoalesce(t *testing.T) {
	plan := sparkwing.NewPlan()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on plan-level Coalesce")
		}
	}()
	plan.Cache(sparkwing.CacheOptions{Key: "k", OnLimit: sparkwing.Coalesce})
}

func TestCacheOptions_PlanLevelRejectsCacheKey(t *testing.T) {
	plan := sparkwing.NewPlan()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on plan-level CacheKey")
		}
	}()
	plan.Cache(sparkwing.CacheOptions{
		Key:      "k",
		CacheKey: func(_ context.Context) sparkwing.CacheKey { return "v" },
	})
}

func TestCacheOptions_NodeLevelCoalesceOK(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(sparkwing.CacheOptions{
		Key:     "k",
		OnLimit: sparkwing.Coalesce,
	})
	if n.CacheOpts().OnLimit != sparkwing.Coalesce {
		t.Fatalf("node-level Coalesce should be allowed")
	}
}

func TestCacheOptions_PlanLevelQueueOK(t *testing.T) {
	plan := sparkwing.NewPlan()
	plan.Cache(sparkwing.CacheOptions{Key: "prod-deploys", Max: 3})
	got := plan.CacheOpts()
	if got.Key != "prod-deploys" || got.Max != 3 {
		t.Fatalf("plan-level cache = %+v", got)
	}
}
