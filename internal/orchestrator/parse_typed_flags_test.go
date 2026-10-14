package orchestrator

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type ptfDemoInputs struct {
	Loud   bool              `flag:"loud" short:"l" desc:"verbose"`
	Target string            `flag:"target" enum:"local,staging,prod" default:"local"`
	Token  string            `flag:"token" secret:"true"`
	Repo   string            `flag:"repo" required:"true"`
	Extras map[string]string `flag:",extra"`
}

type ptfDemoPipe struct{}

func (ptfDemoPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ ptfDemoInputs, _ sparkwing.RunContext) error {
	return nil
}

type ptfPlainInputs struct {
	X string `flag:"x"`
}

type ptfPlainPipe struct{}

func (ptfPlainPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ ptfPlainInputs, _ sparkwing.RunContext) error {
	return nil
}

func init() {
	sparkwing.Register[ptfDemoInputs]("ptf-demo", func() sparkwing.Pipeline[ptfDemoInputs] {
		return ptfDemoPipe{}
	})
	sparkwing.Register[ptfPlainInputs]("ptf-plain", func() sparkwing.Pipeline[ptfPlainInputs] {
		return ptfPlainPipe{}
	})
}

func TestInvoke_RequiredSatisfiedByMergedArgs(t *testing.T) {
	reg, _ := sparkwing.Lookup("ptf-demo")
	if _, err := reg.Invoke(context.Background(), map[string]string{"repo": "r"}, sparkwing.RunContext{Pipeline: "ptf-demo"}); err != nil {
		t.Fatalf("required repo from merged args should satisfy, got %v", err)
	}
	if _, err := reg.Invoke(context.Background(), map[string]string{}, sparkwing.RunContext{Pipeline: "ptf-demo"}); err == nil {
		t.Fatal("missing required repo should still fail at Invoke")
	}
}
