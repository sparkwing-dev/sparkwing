package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var wholeTreeGates = []struct {
	name  string
	check func(context.Context) error
}{
	{"home-resolution", checkHomeResolution},
	{"docs-mirror", checkDocsMirror},
}

const wholeTreeGateBudget = 30 * time.Second

func TestThisRepoSatisfiesItsOwnWholeTreeGates(t *testing.T) {
	root := sourceTreeRoot()
	if root == "" {
		t.Fatal("could not locate the repository root from this file's compile-time path; " +
			"if this package moved, check sourceTreeRoot's markers")
	}

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
