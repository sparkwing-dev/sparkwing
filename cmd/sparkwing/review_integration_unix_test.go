//go:build unix && rewrite_integration

package main

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestReviewProductionExecutionBindings(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	t.Run("local run", func(t *testing.T) {
		err := runLocalCommand(t.Context(), "ci", runFlags{detached: true}, nil, io.Discard,
			localRunDependencies{resolve: resolveLocalRunRuntime, version: "v1.2.3"})
		if err != nil {
			t.Fatalf("public local run cannot reach a concrete owner: %v", err)
		}
	})
	t.Run("local controller", func(t *testing.T) {
		controller, err := openLocalController(t.Context(), t.TempDir())
		if controller != nil {
			t.Cleanup(func() {
				if err := controller.Close(); err != nil {
					t.Error(err)
				}
			})
		}
		if err != nil || controller == nil {
			t.Fatalf("production controller constructor has no concrete owner: %v", err)
		}
	})
	t.Run("local schedules", func(t *testing.T) {
		if err := runScheduleCommand(t.Context(), []string{"install"}, io.Discard, defaultScheduleCommand()); err != nil {
			t.Fatalf("public schedules install has no concrete owner: %v", err)
		}
	})
}

func TestReviewPublicDoctorSweepsAndWarnsForPrunedRun(t *testing.T) {
	p := paths.PathsAt(t.TempDir())
	st, err := teststore.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := st.DB().ExecContext(t.Context(), `INSERT INTO runs
		(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days)
		VALUES ('default','pruned',1,'build','pending','{}','request',1,'github.com/acme/repo','host','execute','[]','member',1,'default',1)`); err != nil {
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
	if _, err := st.DB().ExecContext(t.Context(), "DELETE FROM runs WHERE team='default' AND id='pruned'"); err != nil {
		t.Fatal(err)
	}
	runtime, err := wingd.NewLocalRuntime(wingd.LocalRuntimeConfig{Admission: wingd.Config{Home: p.Root}, Open: openLocalController})
	if err != nil {
		t.Fatal(err)
	}
	services := dashboardServices{runtime: runtime, lifecycle: wingd.NewLocalLifecycle(runtime, nil)}
	var out bytes.Buffer
	err = runDoctorCommand(t.Context(), []string{"-o=json"}, &out, doctorDependencies{
		version: "v1.2.3", machine: func(context.Context) doctorReport { return doctorReport{} },
		local: func(context.Context) (dashboardServices, error) { return services, nil },
	})
	if err != nil {
		t.Fatalf("successful cleanup failed: %v", err)
	}
	if left, err := ledger.List(); err != nil || len(left) != 0 {
		t.Errorf("public doctor did not sweep: %d records, error %v", len(left), err)
	}
	if !strings.Contains(out.String(), "unrecorded_step_outcome") {
		t.Errorf("public doctor hid unrecorded cleanup: %s", &out)
	}
}
