package controller

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestPublicRunStepsCarryAnnotationsAndSummary(t *testing.T) {
	got := publicRunSteps([]*store.NodeStep{{NodeID: "test", StepID: "unit", Attempt: 1, Status: "passed", Annotations: []string{"42 tests"}, Summary: "## Coverage\n91%"}})
	want := controllerwire.RunSteps{Steps: []controllerwire.StepDetail{{
		JobID: "test", ID: "unit", Attempt: 1, Status: "passed", Outcome: "success",
		Annotations: []controllerwire.Annotation{{Message: "42 tests"}}, Summary: "## Coverage\n91%",
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("steps: %+v", got)
	}
}
