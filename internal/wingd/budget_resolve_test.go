package wingd

import (
	"os"
	"path/filepath"
	"testing"
)

// budgetEnvSandbox points the budget config file at a temp directory and
// clears the environment override, so a test starts from a machine with
// no budget set anywhere. It returns the config file path.
func budgetEnvSandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(BudgetEnv, "")
	return filepath.Join(dir, "sparkwing", "budget")
}

// writeBudgetConfig writes the machine-budget config file with the given
// contents.
func writeBudgetConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write budget config: %v", err)
	}
}

// TestResolveBudget_Precedence pins the documented resolution order:
// flag beats env, env beats config file. Every layer is populated with a
// distinguishable value so a win is proved by which one came back, not by
// the absence of the others.
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

// TestResolveBudget_ConfigNeedsNoEnvironment is the point of the whole
// setting: a budget in the config file resolves in a process whose
// environment never carried SPARKWING_BUDGET. The daemon is spawned by
// whichever gate runs first and inherits that process's environment, so a
// setting that needs the variable belongs to whoever happened to trigger
// the spawn rather than to the operator.
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

// TestResolveBudget_UnsetSaysSo is the negative control. With nothing set
// anywhere, the resolver must report the budget as unset rather than
// handing back a zero Budget that reads like a deliberate whole-machine
// choice. Every surface that names the budget keys off this, so a default
// that impersonates a setting would be invisible everywhere at once.
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

// TestResolveBudget_ConfigCommentsAndBlanks checks the config file can
// carry the note explaining why a budget is in force, which is what the
// next person needs when they find admission capped.
func TestResolveBudget_ConfigCommentsAndBlanks(t *testing.T) {
	path := budgetEnvSandbox(t)
	os.Unsetenv(BudgetEnv)
	writeBudgetConfig(t, path, "\n# host sensor over-reads external load, see BW-1454\n\n  ignore-external  \n")

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

// TestResolveBudget_CommentOnlyConfigIsUnset checks a file with nothing
// but a note resolves as unset rather than as a config source with no
// budget behind it.
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

// TestResolveBudget_MalformedConfigFails checks an unparseable config
// value stops the daemon rather than being silently dropped. A setting
// that quietly does nothing is the failure this resolver exists to end,
// and a malformed SPARKWING_BUDGET has always failed the same way.
func TestResolveBudget_MalformedConfigFails(t *testing.T) {
	path := budgetEnvSandbox(t)
	os.Unsetenv(BudgetEnv)
	writeBudgetConfig(t, path, "half of it\n")

	if _, err := ResolveBudget(""); err == nil {
		t.Fatal("resolve accepted a malformed config value; want an error naming the file")
	}
}

// TestConfigResolvedBudget_UnrecordedSourceIsUnknown checks a daemon
// config assembled without the resolver reports its budget source as
// unknown. Naming a source that would not change the budget sends an
// operator to edit the wrong setting, which is worse than admitting the
// source was not recorded.
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
