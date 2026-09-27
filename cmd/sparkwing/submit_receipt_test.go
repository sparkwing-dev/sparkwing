package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestDetachedResubmissionRefusesTriggerWithoutRun(t *testing.T) {
	paths, st := pipelineRefStore(t)
	trigger := store.Trigger{ID: "orphan", Pipeline: "build", IdempotencyKey: "once", CreatedAt: time.Now()}
	if err := st.CreateTrigger(t.Context(), trigger); err != nil {
		t.Fatal(err)
	}
	result, err := persistSubmission(context.Background(), st, paths, submission{
		Pipeline: "build", RepoDir: t.TempDir(), IdempotencyKey: "once", Gate: func(string, string) error { return nil },
	})
	if err == nil {
		t.Fatalf("successful receipt without a run row: %+v", result)
	}
}

func TestDetachedReceiptRequiresRunID(t *testing.T) {
	if err := emitSubmitResult(submitResult{}, "json"); err == nil {
		t.Fatal("empty receipt reported success")
	}
}

func TestDetachedReceiptReportsOutputFailure(t *testing.T) {
	previous := os.Stdout
	file, err := os.CreateTemp(t.TempDir(), "closed-output")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = file
	t.Cleanup(func() { os.Stdout = previous })
	for _, format := range []string{"plain", "pretty", "json"} {
		if err := emitSubmitResult(submitResult{RunHandle: orchestrator.NewRunHandle("run-queued", "build", "", "pending")}, format); err == nil {
			t.Errorf("%s output failure reported success", format)
		}
	}
}

func TestDetachedSubmissionRunFailureLeavesNoTrigger(t *testing.T) {
	paths, st := pipelineRefStore(t)
	if _, err := st.DB().ExecContext(t.Context(), `CREATE TRIGGER reject_run BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT, 'run insert refused'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := persistSubmission(t.Context(), st, paths, submission{
		Pipeline: "build", RepoDir: t.TempDir(), Gate: func(string, string) error { return nil },
	})
	if err == nil {
		t.Fatal("submission accepted a refused run insert")
	}
	triggers, err := st.ListTriggers(t.Context(), store.TriggerFilter{})
	if err != nil || len(triggers) != 0 {
		t.Fatalf("failed submission left runnable triggers: %v, %v", triggers, err)
	}
}
