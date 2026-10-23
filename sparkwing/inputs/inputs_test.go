package inputs_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/inputs"
)

func TestMemoRecipeIsInPlan(t *testing.T) {
	plan := sparkwing.NewPlan()
	recipe := inputs.Compose(inputs.Files("**/*.go"), inputs.Tree("missing"), inputs.Env("MODE"), inputs.Parts("v1", 2))
	sparkwing.Job(plan, "build", func(context.Context) error { return nil }).Memo(recipe)
	job := sdkserve.DefaultOperations.Document(plan).Jobs[0]
	if job.Memo == nil || job.Memo.Input == nil || len(job.Memo.Input.Inputs) != 4 || job.Closures.MemoKey != "" {
		t.Fatalf("recipe absent from plan: %+v", job)
	}
}
