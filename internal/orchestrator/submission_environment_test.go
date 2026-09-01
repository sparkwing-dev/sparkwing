package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

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

func TestSubmissionExecutionEnvironmentForcesHomeAndDropsRunControls(t *testing.T) {
	env := submissionExecutionEnvironment([]string{
		"PATH=/submit/bin",
		"SPARKWING_HOME=/wrong",
		"SPARKWING_RUN_HANDLE_FILE=/tmp/stale.json",
		"SPARKWING_START_AT=late",
		"SPARKWING_SUBMIT_TEST_ENV=kept",
	}, "/queue/home")
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"/wrong", "SPARKWING_RUN_HANDLE_FILE", "SPARKWING_START_AT"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("execution environment contains %q: %s", forbidden, joined)
		}
	}
	for _, required := range []string{"PATH=/submit/bin", "SPARKWING_HOME=/queue/home", "SPARKWING_SUBMIT_TEST_ENV=kept"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("execution environment omits %q: %s", required, joined)
		}
	}
}

func TestReconcileSubmissionEnvironmentsRemovesTerminalSnapshots(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	const runID = "run-terminal-environment"
	if err := CaptureSubmissionEnvironment(home, runID, []string{"TOKEN=secret"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: runID, Pipeline: "build", Status: "pending", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if cancelled, err := st.CancelPendingTrigger(ctx, runID); err != nil || !cancelled {
		t.Fatalf("cancel pending trigger = %v, %v", cancelled, err)
	}
	removed, err := ReconcileSubmissionEnvironments(ctx, home, st, 100)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(submissionEnvironmentPath(home, runID)); !os.IsNotExist(err) {
		t.Fatalf("terminal snapshot still exists: %v", err)
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
