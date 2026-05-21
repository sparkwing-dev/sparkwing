package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// buildOnlyPlan returns a plan with this DAG:
//
//	prep ──┬──> test-phase-a
//	       ├──> test-phase-b-shard-1
//	       └──> test-phase-b-shard-2
//
//	standalone (no deps)
//
// `prep` is the shared prerequisite; the test-phase-* nodes are
// sibling leaves. `standalone` is unrelated.
func buildOnlyPlan(t *testing.T) *sparkwing.Plan {
	t.Helper()
	plan := sparkwing.NewPlan()
	prep := sparkwing.Job(plan, "prep", func(ctx context.Context) error { return nil })
	sparkwing.Job(plan, "test-phase-a", func(ctx context.Context) error { return nil }).Needs(prep)
	sparkwing.Job(plan, "test-phase-b-shard-1", func(ctx context.Context) error { return nil }).Needs(prep)
	sparkwing.Job(plan, "test-phase-b-shard-2", func(ctx context.Context) error { return nil }).Needs(prep)
	sparkwing.Job(plan, "standalone", func(ctx context.Context) error { return nil })
	return plan
}

func TestComputeOnlySkip_EmptyPatternReturnsNil(t *testing.T) {
	plan := buildOnlyPlan(t)
	skip, err := computeOnlySkip(plan, "")
	if err != nil {
		t.Fatalf("computeOnlySkip: %v", err)
	}
	if skip != nil {
		t.Fatalf("empty pattern: want nil skip map; got %v", skip)
	}
}

func TestComputeOnlySkip_GlobMatchesSiblingsPullsSharedAncestor(t *testing.T) {
	plan := buildOnlyPlan(t)
	skip, err := computeOnlySkip(plan, "test-phase-*")
	if err != nil {
		t.Fatalf("computeOnlySkip: %v", err)
	}
	// keep: prep (ancestor) + test-phase-a + test-phase-b-shard-1 + test-phase-b-shard-2
	// skip: standalone
	if _, ok := skip["prep"]; ok {
		t.Errorf("prep should NOT be skipped: ancestor of matched nodes")
	}
	for _, leaf := range []string{"test-phase-a", "test-phase-b-shard-1", "test-phase-b-shard-2"} {
		if _, ok := skip[leaf]; ok {
			t.Errorf("%s should NOT be skipped: matches pattern", leaf)
		}
	}
	if _, ok := skip["standalone"]; !ok {
		t.Errorf("standalone should be skipped: doesn't match and isn't an ancestor")
	}
}

func TestComputeOnlySkip_ExactMatchKeepsOnlyMatchedAndAncestors(t *testing.T) {
	plan := buildOnlyPlan(t)
	skip, err := computeOnlySkip(plan, "test-phase-a")
	if err != nil {
		t.Fatalf("computeOnlySkip: %v", err)
	}
	if _, ok := skip["prep"]; ok {
		t.Errorf("prep should NOT be skipped: ancestor")
	}
	if _, ok := skip["test-phase-a"]; ok {
		t.Errorf("test-phase-a should NOT be skipped: matched")
	}
	for _, sibling := range []string{"test-phase-b-shard-1", "test-phase-b-shard-2", "standalone"} {
		if _, ok := skip[sibling]; !ok {
			t.Errorf("%s should be skipped: not matched, not ancestor", sibling)
		}
	}
}

func TestComputeOnlySkip_NoMatchErrors(t *testing.T) {
	plan := buildOnlyPlan(t)
	_, err := computeOnlySkip(plan, "nonexistent")
	if err == nil {
		t.Fatalf("computeOnlySkip(nonexistent): want error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "matched no jobs") {
		t.Errorf("error %q should mention 'matched no jobs'", msg)
	}
	// The error must list declared ids so the operator can fix the typo.
	for _, id := range []string{"prep", "test-phase-a", "standalone"} {
		if !strings.Contains(msg, id) {
			t.Errorf("error %q should mention declared id %q", msg, id)
		}
	}
}

func TestComputeOnlySkip_MalformedGlobErrors(t *testing.T) {
	plan := buildOnlyPlan(t)
	_, err := computeOnlySkip(plan, "[unclosed")
	if err == nil {
		t.Fatalf("malformed glob: want error, got nil")
	}
}

func TestComputeOnlySkip_NilPlanIsNoop(t *testing.T) {
	skip, err := computeOnlySkip(nil, "test-*")
	if err != nil {
		t.Fatalf("nil plan: %v", err)
	}
	if skip != nil {
		t.Fatalf("nil plan: want nil skip map; got %v", skip)
	}
}

func TestComputeOnlySkip_ReasonStringNamesPattern(t *testing.T) {
	plan := buildOnlyPlan(t)
	skip, err := computeOnlySkip(plan, "test-phase-*")
	if err != nil {
		t.Fatalf("computeOnlySkip: %v", err)
	}
	reason, ok := skip["standalone"]
	if !ok {
		t.Fatalf("standalone should be in skip map")
	}
	if !strings.Contains(reason, "test-phase-*") {
		t.Errorf("skip reason %q should embed the pattern for operator legibility", reason)
	}
}
