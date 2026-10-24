package wingd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestConfigResolvedBudget_UnrecordedSourceIsUnknown(t *testing.T) {
	b, err := userconfig.ParseBudget("4")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := Config{Budget: b}.resolvedBudget()
	if got.Source != wingwire.BudgetSourceUnknown {
		t.Errorf("source = %q, want %q", got.Source, wingwire.BudgetSourceUnknown)
	}

	got = Config{}.resolvedBudget()
	if got.Source != wingwire.BudgetSourceUnset {
		t.Errorf("source with no budget = %q, want %q", got.Source, wingwire.BudgetSourceUnset)
	}

	got = Config{Budget: b, BudgetSource: wingwire.BudgetSourceConfig, BudgetOrigin: "/etc/x"}.resolvedBudget()
	if got.Source != wingwire.BudgetSourceConfig || got.Origin != "/etc/x" {
		t.Errorf("source/origin = %q/%q, want config//etc/x", got.Source, got.Origin)
	}
}

func TestResolveBudgetUsesSharedMachineConfig(t *testing.T) {
	path := writeAdmissionConfig(t, "budget: 50%,ignore-external")
	t.Setenv("SPARKWING_BUDGET", "100")
	got, err := ResolveBudget("")
	if err != nil || got.Source != wingwire.BudgetSourceConfig || got.Origin != path+" admission.budget" || got.Budget.CoresFraction != 0.5 || !got.Budget.IgnoreExternal {
		t.Fatalf("configured budget=%+v error=%v", got, err)
	}
	got, err = ResolveBudget("6")
	if err != nil || got.Source != wingwire.BudgetSourceFlag || got.Origin != "--budget" || got.Budget.Cores != 6 {
		t.Fatalf("explicit budget=%+v error=%v", got, err)
	}
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	got, err = ResolveBudget("")
	if err != nil || got.Source != wingwire.BudgetSourceUnset || got.Budget.IsSet() || got.Origin != "" {
		t.Fatalf("absent budget=%+v error=%v", got, err)
	}
	path = writeAdmissionConfig(t, "budget: invalid")
	if _, err := ResolveBudget(""); err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "admission.budget") {
		t.Fatalf("invalid budget error=%v", err)
	}
	writeAdmissionConfig(t, "budget: 2\nunknown: true")
	if _, err := ResolveBudget(""); err == nil {
		t.Fatal("budget reader accepted an unknown machine-config key")
	}
}
