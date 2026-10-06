//go:build windows

package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestWindowsSubmissionEnvironmentPublishesPrivateSnapshot(t *testing.T) {
	home := t.TempDir()
	const runID = "windows-submission"
	if err := CaptureSubmissionEnvironment(home, runID, []string{"SPARKWING_PROFILE=dev"}, quietLogger()); err != nil {
		t.Fatal(err)
	}
	path := submissionEnvironmentPath(home, runID)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fssecure.VerifyPrivateConfig(path, info); err != nil {
		t.Fatalf("published snapshot is not private: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary snapshot artifacts remain: %v, %v", entries, err)
	}
	env, err := consumeSubmissionEnvironment(home, &store.Trigger{ID: runID, TriggerEnv: map[string]string{SubmissionEnvironmentCapturedKey: "1"}}, quietLogger())
	if err != nil || len(env) != 1 || env[0] != "SPARKWING_PROFILE=dev" {
		t.Fatalf("consumed environment: %v, %v", env, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("consumed snapshot remains: %v", err)
	}
}
