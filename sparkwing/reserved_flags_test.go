package sparkwing

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestReservedFlagNames pins the canonical wing-owned flag set so a
// drift between the SDK list and cmd/sparkwing/wing_flags.go shows up
// as a test failure rather than a silent collision-bug regression.
// When a new wing flag is added, both this test and wingTokenSpecs
// must update in lockstep.
func TestReservedFlagNames(t *testing.T) {
	got := ReservedFlagNames()
	want := []string{
		"C",
		"allow-destructive",
		"allow-money",
		"allow-prod",
		"change-directory",
		"config",
		"dry-run",
		"from",
		"full",
		"mode",
		"no-update",
		"on",
		"retry-of",
		"secrets",
		"start-at",
		"stop-at",
		"v",
		"verbose",
		"workers",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReservedFlagNames() = %v, want %v", got, want)
	}
	// Sorted-set contract: caller may sort/iterate and rely on order.
	if !sort.StringsAreSorted(got) {
		t.Errorf("ReservedFlagNames() not sorted: %v", got)
	}
}

func TestReservedFlagNamesIsCopy(t *testing.T) {
	a := ReservedFlagNames()
	a[0] = "MUTATED"
	b := ReservedFlagNames()
	if b[0] == "MUTATED" {
		t.Errorf("ReservedFlagNames returned a shared slice; mutation leaked: %v", b)
	}
}

// Register must panic when an Args struct declares a flag tag that
// collides with a wing-reserved name. The panic message must name
// the pipeline, the field, the colliding flag, and the full reserved
// list so the author doesn't have to re-discover it.
func TestRegister_PanicsOnReservedFlagCollision(t *testing.T) {
	type args struct {
		From string `flag:"from" desc:"oops collides with wing --from"`
	}
	type pipe struct{}
	// Closure conversion: factory must satisfy func() Pipeline[args].
	var factory = func() Pipeline[args] {
		return wrap[args](func(ctx context.Context, plan *Plan, in args, rc RunContext) error { return nil })
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected Register to panic on reserved-flag collision")
		}
		msg, _ := r.(string)
		// Panic body should include: pipeline name, field name,
		// reserved flag, AND the full reserved list.
		mustContain := []string{
			`"reserved-collision-test"`, // pipeline name
			"--from",                    // colliding flag
			"From",                      // Go field name
			"Reserved wing flags:",      // reserved list header
			"start-at",                  // proves range-resume entries are present
			"stop-at",
			"change-directory",
		}
		for _, sub := range mustContain {
			if !strings.Contains(msg, sub) {
				t.Errorf("panic message missing %q\nfull message: %s", sub, msg)
			}
		}
	}()
	Register[args]("reserved-collision-test", factory)
	_ = pipe{} // keep imported
}

// Register accepts non-colliding flag names; sanity check that the
// validator doesn't over-reject (e.g. `target` is fine).
func TestRegister_AllowsNonReservedFlags(t *testing.T) {
	type args struct {
		Target string `flag:"target" desc:"fine"`
	}
	factory := func() Pipeline[args] {
		return wrap[args](func(ctx context.Context, plan *Plan, in args, rc RunContext) error { return nil })
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Register panicked unexpectedly on non-reserved flag: %v", r)
		}
	}()
	Register[args]("non-colliding-pipeline-test", factory)
}

// pipeFunc adapts a plain Plan-shaped function to the Pipeline[T]
// interface for the table tests above.
type pipeFunc[T any] struct {
	fn func(ctx context.Context, plan *Plan, in T, rc RunContext) error
}

func (p pipeFunc[T]) Plan(ctx context.Context, plan *Plan, in T, rc RunContext) error {
	return p.fn(ctx, plan, in, rc)
}

func wrap[T any](fn func(ctx context.Context, plan *Plan, in T, rc RunContext) error) Pipeline[T] {
	return pipeFunc[T]{fn: fn}
}
