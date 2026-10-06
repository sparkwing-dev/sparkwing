//go:build windows

package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSubmissionWindowsEnvironmentPreservesNativeRuntime(t *testing.T) {
	home := t.TempDir()
	keys := []string{"userprofile", "SystemRoot", "ComSpec", "TEMP", "TMP", "APPDATA", "LOCALAPPDATA", "Path"}
	var original []string
	for _, key := range keys {
		original = append(original, key+"="+os.Getenv(key))
	}
	original = append(original, "SPARKWING_CONFIG="+filepath.Join(home, "config.yaml"), "SPARKWING_SECRETS_KEY_FILE="+filepath.Join(home, "private-key"))
	const runID = "windows-native-env"
	if err := CaptureSubmissionEnvironment(home, runID, original, quietLogger()); err != nil {
		t.Fatal(err)
	}
	captured, err := submissionEnvironment(home, &store.Trigger{ID: runID, TriggerEnv: map[string]string{SubmissionEnvironmentCapturedKey: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(captured, original) {
		t.Fatalf("native environment differs: names=%v", keys)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestSubmissionWindowsEnvironmentChild$")
	command.Env = append(submissionExecutionEnvironment(captured, home), "SPARKWING_TEST_NATIVE_ENV_CHILD=1")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native child runtime: %v\n%s", err, out)
	}
}

func TestSubmissionWindowsEnvironmentChild(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_NATIVE_ENV_CHILD") != "1" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Fatalf("native user home is unavailable: %v", err)
	}
	for _, key := range []string{"SystemRoot", "ComSpec", "TEMP", "TMP", "APPDATA", "LOCALAPPDATA", "PATH"} {
		if os.Getenv(key) == "" {
			t.Fatalf("native runtime lacks %s", key)
		}
	}
}

func TestSubmissionWindowsEnvironmentStillRejectsCredentials(t *testing.T) {
	env := []string{
		"USERPROFILE=Bearer raw-secret",
		"SPARKWING_SECRETS_KEY=raw-secret",
		"SPARKWING_SECRETS_PREVIOUS_KEY=raw-secret",
		"SPARKWING_SECRETS_KEY_FILE=Bearer raw-secret",
		"SPARKWING_SECRETS_KEY_FILE=relative-secret-bytes",
		"SPARKWING_SECRETS_KEY_FILE=C:/private\nAuthorization: Bearer raw-secret",
		"SPARKWING_CONFIG=https://u:p@example.invalid/config",
		"SPARKWING_PROFILE=dev",
	}
	got, err := filterSubmissionEnvironment(env, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "SPARKWING_PROFILE=") {
		t.Fatal("credential-bearing native environment was retained")
	}
}
