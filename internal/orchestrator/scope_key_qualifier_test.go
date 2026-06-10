package orchestrator

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// The qualifier (run id or host) is length-prefixed, so a qualifier or
// name containing a separator can't fold two distinct identities onto
// one key. Under a bare separator, (qualifier="a", name="@b") and
// (qualifier="a@", name="b") collided.
func TestScopeKey_QualifierWithSeparatorDoesNotCollide(t *testing.T) {
	run := func(name string) *sparkwing.ConcurrencyGroup {
		return sparkwing.NewConcurrencyGroup(name, sparkwing.ConcurrencyLimit{Scope: sparkwing.ScopeRun})
	}
	cases := []struct{ qA, nA, qB, nB string }{
		{"a", "@b", "a@", "b"}, // bare '@' boundary
		{"a", ":b", "a:", "b"}, // length-separator ':' boundary
		{"1", ":x", "1:", "x"}, // qualifier that mimics a length prefix
	}
	for _, c := range cases {
		kA := scopedGroupKey(run(c.nA), c.qA)
		kB := scopedGroupKey(run(c.nB), c.qB)
		if kA == kB {
			t.Fatalf("run keys collide: (q=%q,n=%q) and (q=%q,n=%q) both -> %q", c.qA, c.nA, c.qB, c.nB, kA)
		}
	}
}

// ScopeLabelFromKey must recover the full qualifier even when it
// contains the scheme's separators.
func TestScopeKey_LabelRecoversQualifierWithSeparators(t *testing.T) {
	t.Setenv("SPARKWING_BOX_ID", "host:with@seps")
	box := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{Scope: sparkwing.ScopeBox})
	if got := ScopeLabelFromKey(scopedGroupKey(box, "run-1")); got != "box (host:with@seps)" {
		t.Fatalf("box label = %q, want box (host:with@seps)", got)
	}
	run := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{Scope: sparkwing.ScopeRun})
	if got := ScopeLabelFromKey(scopedGroupKey(run, "run:1@x")); got != "run (run:1@x)" {
		t.Fatalf("run label = %q, want run (run:1@x)", got)
	}
}
