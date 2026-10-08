package wingd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func budgetEnvSandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("SPARKWING_CONFIG", "")
	return filepath.Join(dir, "sparkwing", "config.yaml")
}

func writeBudgetConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := fssecure.WriteFile(path, []byte(body)); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestResolveBudget_Precedence(t *testing.T) {
	path := budgetEnvSandbox(t)
	writeBudgetConfig(t, path, "admission:\n  budget: \"2\"\n")
	got, err := ResolveBudget("6")
	if err != nil {
		t.Fatalf("resolve with flag and config set: %v", err)
	}
	if got.Budget.Cores != 6 {
		t.Errorf("cores = %v, want 6: the flag must beat the config file", got.Budget.Cores)
	}
	if got.Source != BudgetSourceFlag {
		t.Errorf("source = %q, want %q", got.Source, BudgetSourceFlag)
	}
	if got.Origin != "--budget" {
		t.Errorf("origin = %q, want --budget", got.Origin)
	}

	t.Setenv("SPARKWING_BUDGET", "4")
	got, err = ResolveBudget("")
	if err != nil {
		t.Fatalf("resolve with the config file and the retired variable set: %v", err)
	}
	if got.Budget.Cores != 2 {
		t.Errorf("cores = %v, want 2: SPARKWING_BUDGET is no longer read", got.Budget.Cores)
	}
	if got.Source != BudgetSourceConfig {
		t.Errorf("source = %q, want %q", got.Source, BudgetSourceConfig)
	}
	if want := path + " admission.budget"; got.Origin != want {
		t.Errorf("origin = %q, want the config path and key %q", got.Origin, want)
	}
}

func TestResolveBudget_ConfigAlone(t *testing.T) {
	path := budgetEnvSandbox(t)
	writeBudgetConfig(t, path, "admission:\n  budget: 50%,ignore-external\n")

	got, err := ResolveBudget("")
	if err != nil {
		t.Fatalf("resolve from config: %v", err)
	}
	if !got.IsSet() {
		t.Fatal("budget is not set: the config file was not read")
	}
	if got.Budget.CoresFraction != 0.5 {
		t.Errorf("cores fraction = %v, want 0.5", got.Budget.CoresFraction)
	}
	if !got.Budget.IgnoreExternal {
		t.Error("ignore-external not read from the config file")
	}
	if want := path + " admission.budget"; got.Source != BudgetSourceConfig || got.Origin != want {
		t.Errorf("source/origin = %q/%q, want %q/%q", got.Source, got.Origin, BudgetSourceConfig, want)
	}
}

func TestResolveBudget_UnsetSaysSo(t *testing.T) {
	budgetEnvSandbox(t)

	got, err := ResolveBudget("")
	if err != nil {
		t.Fatalf("resolve with nothing set: %v", err)
	}
	if got.Source != BudgetSourceUnset {
		t.Errorf("source = %q, want %q: an unconfigured machine must say so", got.Source, BudgetSourceUnset)
	}
	if got.IsSet() {
		t.Error("IsSet() = true with no budget configured anywhere")
	}
	if got.Origin != "" {
		t.Errorf("origin = %q, want empty: there is no setting to name", got.Origin)
	}
	if got.Budget.IsSet() {
		t.Errorf("budget = %+v, want zero", got.Budget)
	}
}

func TestResolveBudget_ConfigCommentsAndBlanks(t *testing.T) {
	path := budgetEnvSandbox(t)
	writeBudgetConfig(t, path, "admission:\n  # host sensor over-reads external load\n\n  budget: \"  ignore-external  \"\n")

	got, err := ResolveBudget("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.Budget.IgnoreExternal {
		t.Errorf("budget = %+v, want ignore-external read past the comment", got.Budget)
	}
	if got.Source != BudgetSourceConfig {
		t.Errorf("source = %q, want %q", got.Source, BudgetSourceConfig)
	}
}

func TestResolveBudget_CommentOnlyConfigIsUnset(t *testing.T) {
	path := budgetEnvSandbox(t)
	writeBudgetConfig(t, path, "admission:\n  # nothing set yet\n  mode: classic\n")

	got, err := ResolveBudget("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Source != BudgetSourceUnset {
		t.Errorf("source = %q, want %q", got.Source, BudgetSourceUnset)
	}
}

func TestResolveBudget_MalformedConfigFails(t *testing.T) {
	path := budgetEnvSandbox(t)
	writeBudgetConfig(t, path, "admission:\n  budget: half of it\n")

	if _, err := ResolveBudget(""); err == nil || !strings.Contains(err.Error(), path+" admission.budget") {
		t.Fatalf("resolve error = %v; want an error naming the file and key", err)
	}
}

func TestResolveBudget_CopiesALegacyBudgetFileIntoConfig(t *testing.T) {
	path := budgetEnvSandbox(t)
	t.Setenv("SPARKWING_HOME", filepath.Dir(filepath.Dir(filepath.Dir(path))))
	legacy := filepath.Join(filepath.Dir(path), "budget")
	writeBudgetConfig(t, legacy, "# leave room for the desktop\n6,enforce\n")

	got, err := ResolveBudget("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Budget.Cores != 6 || !got.Budget.Enforce || got.Source != BudgetSourceConfig {
		t.Fatalf("budget = %+v from %q, want the legacy file's 6,enforce from config.yaml", got.Budget, got.Source)
	}
	if body, err := os.ReadFile(legacy); err != nil || !strings.Contains(string(body), "6,enforce") {
		t.Fatalf("the legacy file = %q, %v; want it left in place for older binaries", body, err)
	}
}

func TestConfigResolvedBudget_UnrecordedSourceIsUnknown(t *testing.T) {
	b, err := ParseBudget("4")
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
