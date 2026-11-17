package nodehost

import (
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"testing"
)

func TestSDKRunContextCarriesStepBounds(t *testing.T) {
	var spec runspec.Spec
	spec.Resolved.Settings.Options.StartAt.Value = "build"
	spec.Resolved.Settings.Options.StopAt.Value = "verify"
	run := executorRunContext(spec)
	if run.StartAt != "build" || run.StopAt != "verify" {
		t.Fatalf("SDK run context bounds = start %q stop %q, want build and verify", run.StartAt, run.StopAt)
	}
}
