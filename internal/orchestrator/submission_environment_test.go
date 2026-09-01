package orchestrator

import (
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSubmissionEnvironmentIsOwnerOnlyAndDiscarded(t *testing.T) {
	home := t.TempDir()
	const runID = "run-environment"
	if err := CaptureSubmissionEnvironment(home, runID, []string{"TOKEN=secret", "MODE=test"}); err != nil {
		t.Fatal(err)
	}

	path := submissionEnvironmentPath(home, runID)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("snapshot mode = %04o, want 0600", got)
	}
	env, err := submissionEnvironment(home, &store.Trigger{
		ID: runID,
		TriggerEnv: map[string]string{
			SubmissionEnvironmentCapturedKey: "1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 2 || env[0] != "TOKEN=secret" || env[1] != "MODE=test" {
		t.Fatalf("snapshot = %#v", env)
	}

	if err := DiscardSubmissionEnvironment(home, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("snapshot still exists after discard: %v", err)
	}
}

func TestSubmissionWithoutCapturedEnvironmentUsesConsumerEnvironment(t *testing.T) {
	env, err := submissionEnvironment(t.TempDir(), &store.Trigger{ID: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if env != nil {
		t.Fatalf("legacy submission environment = %#v, want nil", env)
	}
}
