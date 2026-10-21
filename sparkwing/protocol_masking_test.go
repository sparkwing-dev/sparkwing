package sparkwing

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/secrets/masktest"
)

func TestPlanningDiagnosticsMaskingConformance(t *testing.T) {
	vectors, err := masktest.Load("../internal/secrets/testdata/masking.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			diagnostics := &planningDiagnostics{}
			for _, value := range append(v.Values, v.MergeValues...) {
				diagnostics.values = append(diagnostics.values, diagnosticSecret{value: value})
			}
			for _, value := range v.Lists {
				diagnostics.values = append(diagnostics.values, diagnosticSecret{value: value, list: true})
			}
			source := &apierr.Error{Code: apierr.PlanFailed, Message: v.Input, Hint: v.Input, Details: map[string]any{"nested": []string{v.Input}}}
			var result *apierr.Error
			if !errors.As(diagnostics.sanitize(source, apierr.PlanFailed, ""), &result) {
				t.Fatal("missing diagnostic")
			}
			if result.Message != v.Want || result.Hint != v.Want || result.Details["nested"].([]any)[0] != v.Want {
				t.Fatalf("masking mismatch: %+v", result)
			}
		})
	}
}
