package sparkwing_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type consumeNoOutputsPipe struct{ sparkwing.Base }

func (consumeNoOutputsPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	prod := sparkwing.Job(plan, "build", jobFnNoop())
	sparkwing.Job(plan, "deploy", jobFnNoop()).Consumes(prod)
	return nil
}

func TestInvoke_RejectsConsumeWithoutOutputs(t *testing.T) {
	name := "artifacts-consume-no-outputs"
	sparkwing.Register[sparkwing.NoInputs](name, func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return consumeNoOutputsPipe{}
	})
	reg, ok := sparkwing.Lookup(name)
	if !ok {
		t.Fatal("registered pipeline not found")
	}

	_, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: name})
	if err == nil {
		t.Fatal("Invoke should fail when a consumer's producer declares no Outputs")
	}
	if !strings.Contains(err.Error(), "Outputs") {
		t.Fatalf("error should mention Outputs; got %v", err)
	}
}

type artifactOverlapPipe struct{ sparkwing.Base }

func (artifactOverlapPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	a := sparkwing.Job(plan, "a", jobFnNoop()).Outputs("dist/**")
	b := sparkwing.Job(plan, "b", jobFnNoop()).Outputs("dist/extra.txt")
	sparkwing.Job(plan, "merge", jobFnNoop()).Consumes(a).Consumes(b)
	return nil
}

func TestInvoke_SurfacesArtifactOverlapWarning(t *testing.T) {
	name := "artifacts-overlap-warn"
	sparkwing.Register[sparkwing.NoInputs](name, func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return artifactOverlapPipe{}
	})
	reg, _ := sparkwing.Lookup(name)

	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: name})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var found bool
	for _, w := range plan.LintWarnings() {
		if w.Code == "artifact-stage-overlap" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an artifact-stage-overlap lint warning; got %v", plan.LintWarnings())
	}
}
