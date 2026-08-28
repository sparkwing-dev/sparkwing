package orchestrator

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestScopeKey_GlobalNameWithSeparatorDoesNotCollideWithBox(t *testing.T) {
	t.Setenv("SPARKWING_BOX_ID", "advbox-host")

	global := sparkwing.NewConcurrencyGroup("collide@advbox-host", sparkwing.ConcurrencyLimit{
		Capacity: 1, Scope: sparkwing.ScopeGlobal,
	})
	box := sparkwing.NewConcurrencyGroup("collide", sparkwing.ConcurrencyLimit{
		Capacity: 1, Scope: sparkwing.ScopeBox,
	})

	gk := scopedGroupKey(global, "run-1")
	bk := scopedGroupKey(box, "run-1")
	if gk == bk {
		t.Fatalf("scope-key collision: global %q and box %q fold to the same coordination key", gk, bk)
	}
}

func TestScopeKey_LabelReadsSchemeTagNotSeparator(t *testing.T) {
	t.Setenv("SPARKWING_BOX_ID", "host1")

	cases := []struct {
		group *sparkwing.ConcurrencyGroup
		want  string
	}{
		{sparkwing.NewConcurrencyGroup("payments@db", sparkwing.ConcurrencyLimit{Scope: sparkwing.ScopeGlobal}), "global"},
		{sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{Scope: sparkwing.ScopeBox}), "box (host1)"},
		{sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{Scope: sparkwing.ScopeRun}), "run (run-7)"},
	}
	for _, c := range cases {
		key := scopedGroupKey(c.group, "run-7")
		if got := ScopeLabelFromKey(key); got != c.want {
			t.Errorf("ScopeLabelFromKey(%q) = %q, want %q", key, got, c.want)
		}
	}
}
