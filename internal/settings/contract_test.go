package settings

import (
	"strings"
	"testing"
)

func TestSettingsHaveUniqueKeysAndOrderedSources(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range table() {
		if row.Key == "" || seen[row.Key] {
			t.Errorf("incomplete or duplicate setting: %+v", row)
		}
		seen[row.Key] = true
		switch row.Phase {
		case startup, intake, acceptPlan:
		default:
			t.Errorf("%s: unknown phase %q", row.Key, row.Phase)
		}
		order := precedence()
		if strings.HasPrefix(row.Key, "operator.") || row.Key == "retention" {
			order = operatorPrecedence()
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
	for _, row := range table() {
		for _, source := range row.Sources {
			if (source == projectPipeline || source == projectDefaults || source == code) && row.Phase != acceptPlan {
				t.Errorf("%s resolves checkout input before plan acceptance", row.Key)
			}
		}
	}
}
