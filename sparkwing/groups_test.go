package sparkwing

import (
	"strings"
	"testing"
)

func ctxArgs(args map[string]any) PredicateContext {
	return fakePredCtx{args: args}
}

func TestGroup_ExactlyOne_HoldsForExactlyOneSet(t *testing.T) {
	g := newGroupBuilder([]string{"A", "B", "C"}).ExactlyOne()
	if err := evalGroup(g.meta, ctxArgs(map[string]any{"A": 1})); err != nil {
		t.Fatalf("ExactlyOne with one set should pass; got %v", err)
	}
}

func TestGroup_ExactlyOne_FailsForZeroOrMultiple(t *testing.T) {
	g := newGroupBuilder([]string{"A", "B", "C"}).ExactlyOne()
	if err := evalGroup(g.meta, ctxArgs(map[string]any{})); err == nil {
		t.Fatal("ExactlyOne with none set should fail")
	}
	if err := evalGroup(g.meta, ctxArgs(map[string]any{"A": 1, "B": 2})); err == nil {
		t.Fatal("ExactlyOne with two set should fail")
	}
}

func TestGroup_AtLeastOne(t *testing.T) {
	g := newGroupBuilder([]string{"A", "B"}).AtLeastOne()
	if err := evalGroup(g.meta, ctxArgs(map[string]any{})); err == nil {
		t.Fatal("AtLeastOne with none should fail")
	}
	if err := evalGroup(g.meta, ctxArgs(map[string]any{"A": 1})); err != nil {
		t.Fatalf("AtLeastOne with one should pass; got %v", err)
	}
	if err := evalGroup(g.meta, ctxArgs(map[string]any{"A": 1, "B": 2})); err != nil {
		t.Fatalf("AtLeastOne with all should pass; got %v", err)
	}
}

func TestGroup_AtMostOne(t *testing.T) {
	g := newGroupBuilder([]string{"A", "B", "C"}).AtMostOne()
	if err := evalGroup(g.meta, ctxArgs(map[string]any{})); err != nil {
		t.Fatalf("AtMostOne with none should pass; got %v", err)
	}
	if err := evalGroup(g.meta, ctxArgs(map[string]any{"B": 1})); err != nil {
		t.Fatalf("AtMostOne with one should pass; got %v", err)
	}
	if err := evalGroup(g.meta, ctxArgs(map[string]any{"A": 1, "B": 2})); err == nil {
		t.Fatal("AtMostOne with two should fail")
	}
}

func TestGroup_AllOrNone(t *testing.T) {
	g := newGroupBuilder([]string{"AwsAccessKey", "AwsSecretKey"}).AllOrNone()
	if err := evalGroup(g.meta, ctxArgs(map[string]any{})); err != nil {
		t.Fatalf("AllOrNone with none should pass; got %v", err)
	}
	if err := evalGroup(g.meta, ctxArgs(map[string]any{"AwsAccessKey": "k", "AwsSecretKey": "s"})); err != nil {
		t.Fatalf("AllOrNone with all should pass; got %v", err)
	}
	if err := evalGroup(g.meta, ctxArgs(map[string]any{"AwsAccessKey": "k"})); err == nil {
		t.Fatal("AllOrNone with partial should fail")
	}
}

func TestGroup_WhenGatesActivation(t *testing.T) {
	g := newGroupBuilder([]string{"A"}).
		AtLeastOne().
		When(ArgEq("mode", "deploy"))

	if err := evalGroup(g.meta, ctxArgs(map[string]any{"mode": "deploy"})); err == nil {
		t.Fatal("group should fire when active and unsatisfied")
	}
	if err := evalGroup(g.meta, ctxArgs(map[string]any{"mode": "off"})); err != nil {
		t.Fatalf("dormant group should not fire; got %v", err)
	}
}

func TestGroup_UnsetKindIsAnInternalError(t *testing.T) {
	g := newGroupBuilder([]string{"A", "B"})
	if err := evalGroup(g.meta, ctxArgs(map[string]any{})); err == nil ||
		!strings.Contains(err.Error(), "no cardinality") {
		t.Fatalf("unset kind should error explicitly; got %v", err)
	}
}

func TestGroup_DescOverridesViolationMessage(t *testing.T) {
	g := newGroupBuilder([]string{"A", "B"}).
		AtLeastOne().
		Desc("provide A or B")
	err := evalGroup(g.meta, ctxArgs(map[string]any{}))
	if err == nil || err.Error() != "provide A or B" {
		t.Fatalf("Desc should replace the auto message; got %v", err)
	}
}

func TestGroup_DefaultViolationNamesFieldsAndExpectation(t *testing.T) {
	g := newGroupBuilder([]string{"A", "B", "C"}).ExactlyOne()
	err := evalGroup(g.meta, ctxArgs(map[string]any{"A": 1, "B": 2}))
	if err == nil {
		t.Fatal("expected violation")
	}
	msg := err.Error()
	for _, expect := range []string{"exactly one", "A", "B", "C"} {
		if !strings.Contains(msg, expect) {
			t.Errorf("violation message %q should mention %q", msg, expect)
		}
	}
}
