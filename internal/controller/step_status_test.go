package controller

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestPersistedStepResultsReachTheStepsResponse(t *testing.T) {
	run := consoleRunFixture(t)
	var got controllerwire.RunSteps
	if err := json.Unmarshal(run.get(t, "/api/v1/runs/run-jobs/steps"), &got); err != nil {
		t.Fatal(err)
	}
	type key struct {
		job, step string
		attempt   int64
	}
	steps := map[key]controllerwire.StepDetail{}
	for _, step := range got.Steps {
		steps[key{step.JobID, step.ID, step.Attempt}] = step
	}
	first, second := steps[key{"test", "unit", 1}], steps[key{"test", "unit", 2}]
	if first.Outcome != "failed" || first.Reason != "exit status 1" || first.Summary != "## Attempt 1\n41 passed, 1 failed" ||
		!reflect.DeepEqual(first.Annotations, []nodeproto.Annotation{{Level: "error", Message: "TestCheckout timed out", File: "checkout_test.go", Line: 41}}) {
		t.Fatalf("first attempt's unit step: %+v", first)
	}
	if second.Outcome != "success" || second.Summary != "## Coverage\n91%" ||
		!reflect.DeepEqual(second.Annotations, []nodeproto.Annotation{{Level: "info", Message: "42 tests", File: "checkout_test.go", Line: 7}}) {
		t.Fatalf("second attempt's unit step: %+v", second)
	}
	if setup := steps[key{"test", "setup", 2}]; setup.Outcome != "success" || setup.Annotations != nil {
		t.Fatalf("setup step: %+v", setup)
	}
}

func TestPersistedJobResultsReachRunDetail(t *testing.T) {
	run := consoleRunFixture(t)
	var got controllerwire.RunDetail
	if err := json.Unmarshal(run.get(t, "/api/v1/runs/run-jobs"), &got); err != nil {
		t.Fatal(err)
	}
	jobs := map[string]controllerwire.JobDetail{}
	for _, job := range got.Jobs {
		jobs[job.ID] = job
	}
	test := jobs["test"]
	if test.Attempt != 2 || test.Summary != "## Test report\n42 passed on the second attempt" ||
		!reflect.DeepEqual(test.Annotations, []nodeproto.Annotation{{Level: "warn", Message: "coverage fell 2%", File: "coverage.out", Line: 1, EndLine: 3, Column: 2}}) {
		t.Fatalf("test job: %+v", test)
	}
	if lint := jobs["lint"]; lint.Summary != "" || lint.Annotations != nil {
		t.Fatalf("lint job carries another job's results: %+v", lint)
	}
}
