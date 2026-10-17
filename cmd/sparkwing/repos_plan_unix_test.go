//go:build !windows

package main

import (
	"testing"
)

func TestRepositoryPlanAdapterUsesCurrentSelector(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	projectAt(t, "pipelines:\n  - name: gate\n    entrypoint: Gate\n")
	dir, err := findSparkwingDir()
	if err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, dir, `package main
import (
 "context"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type pipeline struct{ sw.Base }
func (pipeline) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
 sw.Job(p, "verify", func(context.Context) error { return nil })
 return nil
}
func main() { sw.Register("gate", func() sw.Pipeline[sw.NoInputs] { return pipeline{} }); sw.Main() }
`)
	plan, err := (execOps{ctx: t.Context()}).Plan(dir, "gate")
	if err != nil {
		t.Fatalf("plan adapter: %v", err)
	}
	if plan.Pipeline != "gate" || len(plan.Nodes) != 1 || plan.Nodes[0].ID != "verify" {
		t.Fatalf("plan=%+v", plan)
	}
	if _, err := (execOps{ctx: t.Context()}).Plan(dir, "missing"); err == nil {
		t.Fatal("missing baseline accepted")
	}
}
