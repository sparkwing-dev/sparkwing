package sparkwing_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestCache_NotCalledLeavesNilConfig(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{})
	if n.CacheConfig() != nil {
		t.Fatalf("a node without Cache() should have a nil CacheConfig")
	}
}

func TestCache_NilKeyClears(t *testing.T) {
	plan := sparkwing.NewPlan()
	key := func(_ context.Context) sparkwing.CacheKey { return "k" }
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(key)
	if n.CacheConfig() == nil {
		t.Fatalf("Cache(key) should register a config")
	}
	n.Cache(nil)
	if n.CacheConfig() != nil {
		t.Fatalf("Cache(nil) should clear the config")
	}
}

func TestCache_DefaultsTTL(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(
		func(_ context.Context) sparkwing.CacheKey { return "k" })
	cfg := n.CacheConfig()
	if cfg == nil {
		t.Fatal("expected a cache config")
	}
	if cfg.TTL != sparkwing.DefaultCacheTTL {
		t.Fatalf("TTL = %s, want DefaultCacheTTL", cfg.TTL)
	}
}

func TestCache_TTLOptionApplied(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(
		func(_ context.Context) sparkwing.CacheKey { return "k" },
		sparkwing.TTL(2*time.Hour))
	if got := n.CacheConfig().TTL; got != 2*time.Hour {
		t.Fatalf("TTL = %s, want 2h", got)
	}
}

func TestCache_ClampsLongTTL(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(
		func(_ context.Context) sparkwing.CacheKey { return "k" },
		sparkwing.TTL(365*24*time.Hour))
	if got := n.CacheConfig().TTL; got != sparkwing.MaxCacheTTL {
		t.Fatalf("TTL = %s, want MaxCacheTTL", got)
	}
}

func TestCache_NonPositiveTTLFallsBackToDefault(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(
		func(_ context.Context) sparkwing.CacheKey { return "k" },
		sparkwing.TTL(-time.Second))
	if got := n.CacheConfig().TTL; got != sparkwing.DefaultCacheTTL {
		t.Fatalf("TTL = %s, want DefaultCacheTTL", got)
	}
}

func TestCache_KeyFnRetained(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "x", &buildJob{}).Cache(
		func(_ context.Context) sparkwing.CacheKey { return sparkwing.Key("coverage", "shard-1") })
	cfg := n.CacheConfig()
	if cfg == nil || cfg.Key == nil {
		t.Fatal("expected a retained key function")
	}
	if got := cfg.Key(context.Background()); got != sparkwing.Key("coverage", "shard-1") {
		t.Fatalf("key fn returned %q", got)
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
