package sparkwing

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestPlanningDiagnosticsMaskingConformance(t *testing.T) {
	raw, err := os.ReadFile("../internal/secrets/testdata/masking.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name        string
		Values      []string
		Input, Want string
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			diagnostics := &planningDiagnostics{values: v.Values}
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
