package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestMachineSettings_BareWildcardNamesTheKey(t *testing.T) {
	path := writeMachineSettings(t, "run:\n  submit_env_allow: [\"*\"]\n")
	_, _, err := submissionEnvironmentAllowList()
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), submitEnvAllowKey) {
		t.Fatalf("error = %v, want it to name %s and %s", err, path, submitEnvAllowKey)
	}
}
