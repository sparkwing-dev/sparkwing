//go:build unix && rewrite_integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestReviewProductionExecutionBindings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	offlineStopDaemon(t, home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	t.Run("local run", func(t *testing.T) {
		err := runSparkwing([]string{"run", "example", "-o=json"})
		if err != nil {
			t.Fatalf("public local run cannot reach a concrete owner: %v", err)
		}
	})
	t.Run("local controller", func(t *testing.T) {
		services, err := resolveLocalServicesAt(t.Context(), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancelCause(t.Context())
		done := make(chan error, 1)
		go func() {
			err := services.runtime.Run(ctx, services.options)
			done <- err
			cancel(err)
		}()
		t.Cleanup(func() {
			cancel(context.Canceled)
			if err := services.runtime.Drain(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		})
		status, err := services.runtime.AwaitReady(ctx)
		cancel(context.Canceled)
		joined := <-done
		if err != nil {
			t.Fatalf("production controller cannot establish readiness: %v", errors.Join(err, joined))
		}
		if !status.Running || joined != nil && !errors.Is(joined, context.Canceled) {
			t.Fatalf("production controller did not join: %+v, %v", status, joined)
		}
	})
	t.Run("local schedules", func(t *testing.T) {
		if err := runScheduleCommandArgs(t.Context(), []string{"install"}, io.Discard, defaultScheduleCommand()); err != nil {
			t.Fatalf("public schedules install has no concrete owner: %v", err)
		}
	})
}

func TestReviewPublicDoctorSweepsAndWarnsForPrunedRun(t *testing.T) {
	p := paths.PathsAt(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", p.Root)
	offlineStopDaemon(t, p.Root)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	st, err := teststore.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if st != nil {
			if err := st.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := teststore.SQL(t, st).ExecContext(t.Context(), `INSERT INTO runs
		(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days)
		VALUES ('default','pruned',1,'build','pending','{}','request',1,'github.com/acme/repo','host','execute','member',1,'default',1)`); err != nil {
		t.Fatal(err)
	}
	owner := exec.Command("true")
	if err := owner.Run(); err != nil {
		t.Fatal(err)
	}
	ledger := sessionledger.Open(p.SessionLedgerDir())
	if _, err := ledger.Record(sessionledger.Record{
		Run: "pruned", Node: "build", OwnerPID: owner.Process.Pid, OwnerBirth: "gone",
		Handle: sessionledger.Handle{Kind: "command", ID: "cleanup", Argv: []string{"true"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, st).ExecContext(t.Context(), "DELETE FROM runs WHERE team='default' AND id='pruned'"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = nil
	out := captureStdout(t, func() {
		err = runDoctorArgs([]string{"-o=json"})
	})
	if err != nil {
		t.Fatalf("successful cleanup failed: %v", err)
	}
	if left, err := ledger.List(); err != nil || len(left) != 0 {
		t.Errorf("public doctor did not sweep: %d records, error %v", len(left), err)
	}
	var report doctorReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("doctor report: %v; output %s", err, out)
	}
	if !report.ControllerChecked || report.ControllerError != "" {
		t.Errorf("controller diagnostics incomplete: %+v", report)
	}
	for _, finding := range report.Findings {
		if finding.Code == "unrecorded_step_outcome" && finding.Message != "" {
			return
		}
	}
	t.Errorf("public doctor hid unrecorded cleanup: %s", out)
}
