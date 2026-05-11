package sparkwing_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestWork_GroupsAccessorReturnsDeclaredGroupsInOrder(t *testing.T) {
	w := sparkwing.NewWork()
	fetch := sparkwing.Step(w, "fetch", func(context.Context) error { return nil })
	lint := sparkwing.Step(w, "lint", func(context.Context) error { return nil }).Needs(fetch)
	vet := sparkwing.Step(w, "vet", func(context.Context) error { return nil }).Needs(fetch)
	test := sparkwing.Step(w, "test", func(context.Context) error { return nil }).Needs(fetch)
	smoke := sparkwing.Step(w, "smoke", func(context.Context) error { return nil })
	bench := sparkwing.Step(w, "bench", func(context.Context) error { return nil })

	checks := sparkwing.GroupSteps(w, "checks", lint, vet, test)
	verify := sparkwing.GroupSteps(w, "verify", smoke, bench)

	got := w.Groups()
	if len(got) != 2 {
		t.Fatalf("Groups() len = %d, want 2", len(got))
	}
	if got[0] != checks {
		t.Errorf("Groups()[0] = %p, want checks (%p)", got[0], checks)
	}
	if got[1] != verify {
		t.Errorf("Groups()[1] = %p, want verify (%p)", got[1], verify)
	}
	if got[0].Name() != "checks" {
		t.Errorf("Groups()[0].Name() = %q, want %q", got[0].Name(), "checks")
	}
	wantMembers := []string{"lint", "vet", "test"}
	gotMembers := make([]string, 0, len(got[0].Members()))
	for _, m := range got[0].Members() {
		gotMembers = append(gotMembers, m.ID())
	}
	if !reflect.DeepEqual(gotMembers, wantMembers) {
		t.Errorf("Groups()[0] members = %v, want %v", gotMembers, wantMembers)
	}
}

func TestWork_GroupsEmptyWhenNoGroupSteps(t *testing.T) {
	w := sparkwing.NewWork()
	sparkwing.Step(w, "only", func(context.Context) error { return nil })
	if got := w.Groups(); len(got) != 0 {
		t.Errorf("Groups() = %v, want empty", got)
	}
}
