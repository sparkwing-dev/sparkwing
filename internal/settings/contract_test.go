package settings

import (
	"strings"
	"testing"
)

func TestSettingsHaveUniqueKeysAndOrderedSources(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range Table() {
		if row.Key == "" || seen[row.Key] {
			t.Errorf("incomplete or duplicate setting: %+v", row)
		}
		seen[row.Key] = true
		switch row.Phase {
		case Startup, Intake, AcceptPlan:
		default:
			t.Errorf("%s: unknown phase %q", row.Key, row.Phase)
		}
		order := Precedence()
		if strings.HasPrefix(row.Key, "operator.") || row.Key == "retention" {
			order = OperatorPrecedence()
		}
		last := -1
		for _, source := range row.Sources {
			index := -1
			for i, candidate := range order {
				if candidate == source {
					index = i
				}
			}
			if index <= last {
				t.Errorf("%s: source %q violates fixed precedence", row.Key, source)
			}
			last = index
		}
	}
}

func TestCheckoutAndCodeSettingsWaitForAcceptance(t *testing.T) {
	for _, row := range Table() {
		for _, source := range row.Sources {
			if (source == ProjectPipeline || source == ProjectDefaults || source == Code) && row.Phase != AcceptPlan {
				t.Errorf("%s resolves checkout input before plan acceptance", row.Key)
			}
		}
	}
}
