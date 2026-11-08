package jobs

import (
	"context"
	"testing"
	"time"
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
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	root := sourceTreeRoot()
	if root == "" {
		t.Fatal("could not locate the repository root from this file's compile-time path; " +
			"if this package moved, check sourceTreeRoot's markers")
	}

	t.Chdir(root)

	ctx, cancel := context.WithTimeout(newTestContext(t), wholeTreeGateBudget)
	defer cancel()
	start := time.Now()
	defer func() { t.Logf("whole-tree gates took %s", time.Since(start)) }()
	for _, g := range wholeTreeGates {
		t.Run(g.name, func(t *testing.T) {
			if err := g.check(ctx); err != nil {
				t.Fatalf("%s fails against %s:\n%v", g.name, root, err)
			}
		})
	}
}
