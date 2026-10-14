package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeMachineSettings(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", path)
	return path
}

func TestMachineSettings_ReadConfigNotEnvironment(t *testing.T) {
	t.Setenv("SPARKWING_LOGS_DROP_POLICY", "warn")
	t.Setenv("SPARKWING_PAUSE_TIMEOUT", "1s")
	writeMachineSettings(t, "repos: {}\n")
	if !logsDropIsFatal() {
		t.Error("SPARKWING_LOGS_DROP_POLICY still read")
	}
	if got := pauseTimeout(); got != defaultPauseTimeout {
		t.Errorf("pause timeout = %v, want the default; SPARKWING_PAUSE_TIMEOUT is no longer read", got)
	}

	writeMachineSettings(t, "logs:\n  drop_policy: warn\ndebug:\n  pause_timeout: 200ms\nrun:\n  submit_env_allow: [AWS_REGION, DOCKER_*]\n")
	if logsDropIsFatal() {
		t.Error("logs.drop_policy: warn did not keep dropped lines non-fatal")
	}
	if got := pauseTimeout(); got != 200*time.Millisecond {
		t.Errorf("pause timeout = %v, want 200ms", got)
	}
	names, prefixes, err := submissionEnvironmentAllowList()
	if err != nil || !names["AWS_REGION"] || len(prefixes) != 1 || prefixes[0] != "DOCKER_" {
		t.Errorf("allow list = %v %v %v, want AWS_REGION and DOCKER_", names, prefixes, err)
	}
}

func TestMachineSettings_UnknownKeyKeepsLogDropsFatal(t *testing.T) {
	writeMachineSettings(t, "logs:\n  drop_polcy: warn\n")
	if !logsDropIsFatal() {
		t.Error("a misspelled key relaxed the log drop policy")
	}
}

func TestMachineSettings_BareWildcardNamesTheKey(t *testing.T) {
	path := writeMachineSettings(t, "run:\n  submit_env_allow: [\"*\"]\n")
	_, _, err := submissionEnvironmentAllowList()
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), submitEnvAllowKey) {
		t.Fatalf("error = %v, want it to name %s and %s", err, path, submitEnvAllowKey)
	}
}
