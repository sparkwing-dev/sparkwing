package wingd

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestConfigResolvedBudget_UnrecordedSourceIsUnknown(t *testing.T) {
	b, err := userconfig.ParseBudget("4")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := Config{Budget: b}.resolvedBudget()
	if got.Source != BudgetSourceUnknown {
		t.Errorf("source = %q, want %q", got.Source, BudgetSourceUnknown)
	}

	got = Config{}.resolvedBudget()
	if got.Source != BudgetSourceUnset {
		t.Errorf("source with no budget = %q, want %q", got.Source, BudgetSourceUnset)
	}

	got = Config{Budget: b, BudgetSource: BudgetSourceConfig, BudgetOrigin: "/etc/x"}.resolvedBudget()
	if got.Source != BudgetSourceConfig || got.Origin != "/etc/x" {
		t.Errorf("source/origin = %q/%q, want config//etc/x", got.Source, got.Origin)
	}
}
