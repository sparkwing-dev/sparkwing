package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// wholeTreeGates are the pre-commit steps that judge the whole
// repository rather than the staged change. They are the gates whose
// premise is that this tree already satisfies them -- a whole-tree sweep
// is only affordable because it has no pre-existing corpus to charge an
// unrelated commit for -- so the premise is worth a standing assertion
// rather than a hope.
//
// Add a gate here when it sweeps the tree. Each entry must be read-only
// and cheap: the whole table runs on every `go test ./...` of this
// module, including the one inside `sparkwing run pre-commit`, so it has
// to stay in the regex-sweep cost tier and must never invoke the
// pipeline that contains it.
var wholeTreeGates = []struct {
	name  string
	check func(context.Context) error
}{
	{"home-resolution", checkHomeResolution},
	{"docs-mirror", checkDocsMirror},
}

// wholeTreeGateBudget is what the whole table is allowed to cost. The
// gates it holds are the parallel sweeps, measured in hundredths of a
// second; anything approaching a second means something in the table
// started doing real work and belongs in the pipeline instead.
const wholeTreeGateBudget = time.Second

// TestThisRepoSatisfiesItsOwnWholeTreeGates runs the whole-tree gates
// against the real repository.
//
// Before this existed, every author changing a whole-tree gate verified
// it the same way: a throwaway test with an absolute worktree path typed
// into it, run once, deleted. That answered "does my change work here,
// now" and nothing else. This answers it on every run, in every
// checkout, for free.
func TestThisRepoSatisfiesItsOwnWholeTreeGates(t *testing.T) {
	root := sourceTreeRoot()
	if root == "" {
		t.Fatal("could not locate the repository root from this file's compile-time path; " +
			"if this package moved, check sourceTreeRoot's markers")
	}

	// The gates read the tree through the run's working directory, which
	// no sparkwing run has set here.
	prev := sparkwing.WorkDir()
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	start := time.Now()
	for _, g := range wholeTreeGates {
		t.Run(g.name, func(t *testing.T) {
			if err := g.check(context.Background()); err != nil {
				t.Fatalf("%s fails against %s:\n%v", g.name, root, err)
			}
		})
	}
	if elapsed := time.Since(start); elapsed > wholeTreeGateBudget {
		t.Errorf("the whole-tree gates took %s, over the %s budget for this table",
			elapsed.Round(time.Millisecond), wholeTreeGateBudget)
	}
}
