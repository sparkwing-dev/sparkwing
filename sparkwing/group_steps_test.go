package sparkwing

import (
	"context"
	"reflect"
	"testing"
)

func TestWork_GroupsAccessorReturnsDeclaredGroupsInOrder(t *testing.T) {
	w := NewWork()
	fetch := Step(w, "fetch", func(context.Context) error { return nil })
	lint := Step(w, "lint", func(context.Context) error { return nil }).Needs(fetch)
	vet := Step(w, "vet", func(context.Context) error { return nil }).Needs(fetch)
	test := Step(w, "test", func(context.Context) error { return nil }).Needs(fetch)
	smoke := Step(w, "smoke", func(context.Context) error { return nil })
	bench := Step(w, "bench", func(context.Context) error { return nil })

	checks := GroupSteps(w, "checks", lint, vet, test)
	verify := GroupSteps(w, "verify", smoke, bench)

	got := w.groups
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
	w := NewWork()
	Step(w, "only", func(context.Context) error { return nil })
	if got := w.groups; len(got) != 0 {
		t.Errorf("Groups() = %v, want empty", got)
	}
}
