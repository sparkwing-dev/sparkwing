package wingd

import (
	"os"
	"path/filepath"
	"testing"
)

func budgetEnvSandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(BudgetEnv, "")
	return filepath.Join(dir, "sparkwing", "budget")
}

func writeBudgetConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write budget config: %v", err)
	}
}

func TestResolveBudget_Precedence(t *testing.T) {
	path := budgetEnvSandbox(t)
	writeBudgetConfig(t, path, "2\n")
	t.Setenv(BudgetEnv, "4")

	got, err := ResolveBudget("6")
	if err != nil {
		t.Fatalf("resolve with all three set: %v", err)
	}
	if got.Budget.Cores != 6 {
		t.Errorf("cores = %v, want 6: the flag must beat the environment and the config file", got.Budget.Cores)
	}
	if got.Source != BudgetSourceFlag {
		t.Errorf("source = %q, want %q", got.Source, BudgetSourceFlag)
	}
	if got.Origin != "--budget" {
		t.Errorf("origin = %q, want --budget", got.Origin)
	}

	got, err = ResolveBudget("")
	if err != nil {
		t.Fatalf("resolve with env and config set: %v", err)
	}
	if got.Budget.Cores != 4 {
		t.Errorf("cores = %v, want 4: the environment must beat the config file", got.Budget.Cores)
	}
	if got.Source != BudgetSourceEnv {
		t.Errorf("source = %q, want %q", got.Source, BudgetSourceEnv)
	}
	if got.Origin != BudgetEnv {
		t.Errorf("origin = %q, want %q", got.Origin, BudgetEnv)
	}

	t.Setenv(BudgetEnv, "")
	got, err = ResolveBudget("")
	if err != nil {
		t.Fatalf("resolve with only the config file set: %v", err)
	}
	if got.Budget.Cores != 2 {
		t.Errorf("cores = %v, want 2: the config file must apply when nothing else is set", got.Budget.Cores)
	}
	if got.Source != BudgetSourceConfig {
		t.Errorf("source = %q, want %q", got.Source, BudgetSourceConfig)
	}
	if got.Origin != path {
		t.Errorf("origin = %q, want the config path %q", got.Origin, path)
	}
}

func TestResolveBudget_ConfigNeedsNoEnvironment(t *testing.T) {
	path := budgetEnvSandbox(t)
	os.Unsetenv(BudgetEnv)
	writeBudgetConfig(t, path, "50%,ignore-external\n")

	got, err := ResolveBudget("")
	if err != nil {
		t.Fatalf("resolve from config: %v", err)
	}
	if !got.IsSet() {
		t.Fatal("budget is not set: the config file did not survive an environment that never exported the variable")
	}
	if got.Budget.CoresFraction != 0.5 {
		t.Errorf("cores fraction = %v, want 0.5", got.Budget.CoresFraction)
	}
	if !got.Budget.IgnoreExternal {
		t.Error("ignore-external not read from the config file")
	}
	if got.Source != BudgetSourceConfig || got.Origin != path {
		t.Errorf("source/origin = %q/%q, want %q/%q", got.Source, got.Origin, BudgetSourceConfig, path)
	}
}

func TestResolveBudget_UnsetSaysSo(t *testing.T) {
	budgetEnvSandbox(t)
	os.Unsetenv(BudgetEnv)

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
	os.Unsetenv(BudgetEnv)
	writeBudgetConfig(t, path, "\n# host sensor over-reads external load\n\n  ignore-external  \n")

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
	os.Unsetenv(BudgetEnv)
	writeBudgetConfig(t, path, "# nothing set yet\n")

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
	os.Unsetenv(BudgetEnv)
	writeBudgetConfig(t, path, "half of it\n")

	if _, err := ResolveBudget(""); err == nil {
		t.Fatal("resolve accepted a malformed config value; want an error naming the file")
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
