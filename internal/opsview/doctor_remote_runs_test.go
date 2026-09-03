package opsview_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// seedRunDirHome builds a home with one recorded run and one run directory
// whose row is nowhere in the local store.
func seedRunDirHome(t *testing.T, ghostAge time.Duration) (paths.Paths, string) {
	t.Helper()
	home := shortHome(t)
	p := paths.PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.CreateRun(context.Background(), store.Run{
		ID: "run-known", Pipeline: "demo", Status: "success", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = st.Close()

	ghost := filepath.Join(p.RunsDir(), "run-elsewhere")
	if err := os.MkdirAll(ghost, 0o700); err != nil {
		t.Fatalf("make run dir: %v", err)
	}
	envelope := filepath.Join(ghost, "_envelope.ndjson")
	if err := os.WriteFile(envelope, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write envelope: %v", err)
	}
	stamp := time.Now().Add(-ghostAge)
	for _, path := range []string{envelope, ghost} {
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatalf("backdate %s: %v", path, err)
		}
	}
	return p, ghost
}

func writeProfiles(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write profiles: %v", err)
		}
	}
	t.Setenv("SPARKWING_PROFILES", path)
}

func TestDiagnose_KeepsRunDirWhenAProfileRecordsRunsElsewhere(t *testing.T) {
	writeProfiles(t, `profiles:
  fleet:
    state:
      type: s3
      bucket: team
      prefix: state
    logs:
      type: s3
      bucket: team
      prefix: logs
    mirror_local: false
`)
	p, ghost := seedRunDirHome(t, time.Hour)

	report, err := opsview.Diagnose(context.Background(), p, p.Root, "v1.0.0", false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(report.DanglingRunDirs) != 0 {
		t.Errorf("dangling run dirs = %v, want none while a profile keeps run rows elsewhere", report.DanglingRunDirs)
	}
	if _, err := os.Stat(filepath.Join(ghost, "_envelope.ndjson")); err != nil {
		t.Fatalf("doctor deleted a run directory whose row lives in a remote store: %v", err)
	}
	if len(report.UnknownRunDirs) != 1 || report.UnknownRunDirs[0] != "run-elsewhere" {
		t.Errorf("unknown run dirs = %v, want [run-elsewhere]", report.UnknownRunDirs)
	}
}

func TestDiagnose_KeepsRunDirWhenLocalStoreHasNoRuns(t *testing.T) {
	writeProfiles(t, "")
	home := shortHome(t)
	p := paths.PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_ = st.Close()
	ghost := filepath.Join(p.RunsDir(), "run-elsewhere")
	if err := os.MkdirAll(ghost, 0o700); err != nil {
		t.Fatalf("make run dir: %v", err)
	}
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(ghost, stamp, stamp); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	report, err := opsview.Diagnose(context.Background(), p, p.Root, "v1.0.0", false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if _, err := os.Stat(ghost); err != nil {
		t.Fatalf("doctor deleted a run directory though no run was ever recorded locally: %v", err)
	}
	if len(report.UnknownRunDirs) != 1 || report.UnknownRunDirs[0] != "run-elsewhere" {
		t.Errorf("unknown run dirs = %v, want [run-elsewhere]", report.UnknownRunDirs)
	}
}

func TestDiagnose_KeepsARunDirStillInsideTheStartGrace(t *testing.T) {
	writeProfiles(t, "")
	p, ghost := seedRunDirHome(t, 0)

	report, err := opsview.Diagnose(context.Background(), p, p.Root, "v1.0.0", false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if _, err := os.Stat(ghost); err != nil {
		t.Fatalf("doctor deleted a run directory a starting run had just created: %v", err)
	}
	if len(report.DanglingRunDirs) != 0 || len(report.UnknownRunDirs) != 0 {
		t.Errorf("a directory inside the grace was reported: dangling=%v unknown=%v",
			report.DanglingRunDirs, report.UnknownRunDirs)
	}
}

func TestDiagnose_RemovesAnIdleRunDirTheLocalStoreOwns(t *testing.T) {
	writeProfiles(t, "")
	p, ghost := seedRunDirHome(t, time.Hour)

	report, err := opsview.Diagnose(context.Background(), p, p.Root, "v1.0.0", false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(report.DanglingRunDirs) != 1 || report.DanglingRunDirs[0] != "run-elsewhere" {
		t.Fatalf("dangling run dirs = %v, want [run-elsewhere]", report.DanglingRunDirs)
	}
	if _, err := os.Stat(ghost); !os.IsNotExist(err) {
		t.Fatalf("dangling run directory survived a sweep the local store owns: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.RunsDir(), "run-known")); err == nil {
		t.Fatal("test setup created a directory for the recorded run")
	}
}
