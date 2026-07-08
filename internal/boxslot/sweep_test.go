package boxslot_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
)

const sweepChildRunID = "run-20260701-000000-5weep001"

func TestStallTTL_EnvResolution(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		want    time.Duration
		wantErr bool
	}{
		{"unset keeps default", "", boxslot.DefaultStallTTL, false},
		{"duration overrides", "10m", 10 * time.Minute, false},
		{"unparseable errors", "soon", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(boxslot.StallTTLEnvVar, tc.env)

			got, err := boxslot.StallTTL()

			if tc.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				if !strings.Contains(err.Error(), boxslot.StallTTLEnvVar) || !strings.Contains(err.Error(), tc.env) {
					t.Errorf("error %q does not name the variable and value", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("StallTTL: %v", err)
			}
			if got != tc.want {
				t.Errorf("ttl = %s, want %s", got, tc.want)
			}
		})
	}
}

// seedEnvelope creates <runsDir>/<runID>/_envelope.ndjson with the
// given mtime.
func seedEnvelope(t *testing.T, runsDir, runID string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(runsDir, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	path := filepath.Join(dir, "_envelope.ndjson")
	if err := os.WriteFile(path, []byte(`{"event":"run_start"}`+"\n"), 0o644); err != nil {
		t.Fatalf("seed envelope: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes envelope: %v", err)
	}
	return path
}

func TestSweepStalled_EnvelopeMtimeDecides(t *testing.T) {
	cases := []struct {
		name        string
		envelopeAge time.Duration
		wantStalled bool
	}{
		{"silent past ttl is stalled", 2 * time.Hour, true},
		{"recently written is healthy", time.Minute, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lockDir, runsDir := t.TempDir(), t.TempDir()
			release, err := boxslot.Acquire(context.Background(), boxslot.Options{MaxSlots: 1, LockDir: lockDir})
			if err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			defer release()
			if err := boxslot.AnnotateHolder(lockDir, sweepChildRunID); err != nil {
				t.Fatalf("AnnotateHolder: %v", err)
			}
			seedEnvelope(t, runsDir, sweepChildRunID, time.Now().Add(-tc.envelopeAge))

			stalled, err := boxslot.SweepStalled(lockDir, runsDir, 30*time.Minute)

			if err != nil {
				t.Fatalf("SweepStalled: %v", err)
			}
			if !tc.wantStalled {
				if len(stalled) != 0 {
					t.Fatalf("healthy holder reported stalled: %+v", stalled)
				}
				return
			}
			if len(stalled) != 1 {
				t.Fatalf("stalled = %+v, want one row", stalled)
			}
			s := stalled[0]
			if s.PID != os.Getpid() || s.RunID != sweepChildRunID || !s.Live {
				t.Errorf("row = %+v, want own live pid with run %s", s, sweepChildRunID)
			}
			if s.Age < 30*time.Minute {
				t.Errorf("Age = %s, want the envelope's silence age", s.Age)
			}
			for _, want := range []string{sweepChildRunID, "_envelope.ndjson"} {
				if !strings.Contains(s.Evidence, want) {
					t.Errorf("evidence %q missing %q", s.Evidence, want)
				}
			}
		})
	}
}

func TestSweepStalled_NewestRunFileCorroboratesButNeverDecides(t *testing.T) {
	lockDir, runsDir := t.TempDir(), t.TempDir()
	release, err := boxslot.Acquire(context.Background(), boxslot.Options{MaxSlots: 1, LockDir: lockDir})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()
	if err := boxslot.AnnotateHolder(lockDir, sweepChildRunID); err != nil {
		t.Fatalf("AnnotateHolder: %v", err)
	}
	seedEnvelope(t, runsDir, sweepChildRunID, time.Now().Add(-2*time.Hour))
	nodeLog := filepath.Join(runsDir, sweepChildRunID, "nodes", "build.log")
	if err := os.MkdirAll(filepath.Dir(nodeLog), 0o755); err != nil {
		t.Fatalf("mkdir node dir: %v", err)
	}
	if err := os.WriteFile(nodeLog, []byte("tick\n"), 0o644); err != nil {
		t.Fatalf("seed node log: %v", err)
	}

	stalled, err := boxslot.SweepStalled(lockDir, runsDir, 30*time.Minute)

	if err != nil {
		t.Fatalf("SweepStalled: %v", err)
	}
	if len(stalled) != 1 {
		t.Fatalf("stalled = %+v, want one row (the verdict stays envelope-based)", stalled)
	}
	s := stalled[0]
	if s.NewestFile != nodeLog {
		t.Errorf("NewestFile = %q, want the fresh node log %q", s.NewestFile, nodeLog)
	}
	if s.NewestFileAge > time.Minute {
		t.Errorf("NewestFileAge = %s, want the node log's fresh mtime age", s.NewestFileAge)
	}
	if s.Age < 30*time.Minute {
		t.Errorf("Age = %s, want the envelope's silence age untouched by the node log", s.Age)
	}
}

func TestSweepStalled_NoRunAnnotatedUsesClaimTime(t *testing.T) {
	lockDir, runsDir := t.TempDir(), t.TempDir()
	release, err := boxslot.Acquire(context.Background(), boxslot.Options{MaxSlots: 1, LockDir: lockDir})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	if stalled, err := boxslot.SweepStalled(lockDir, runsDir, 30*time.Minute); err != nil || len(stalled) != 0 {
		t.Fatalf("fresh unannotated holder: stalled=%+v err=%v, want none", stalled, err)
	}

	time.Sleep(10 * time.Millisecond)
	stalled, err := boxslot.SweepStalled(lockDir, runsDir, time.Millisecond)
	if err != nil {
		t.Fatalf("SweepStalled: %v", err)
	}
	if len(stalled) != 1 {
		t.Fatalf("stalled = %+v, want one row", stalled)
	}
	if !strings.Contains(stalled[0].Evidence, "no run annotated") {
		t.Errorf("evidence %q missing the no-run reason", stalled[0].Evidence)
	}
}

func TestSweepStalled_AnnotatedButMissingEnvelopeUsesClaimTime(t *testing.T) {
	lockDir, runsDir := t.TempDir(), t.TempDir()
	release, err := boxslot.Acquire(context.Background(), boxslot.Options{MaxSlots: 1, LockDir: lockDir})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()
	if err := boxslot.AnnotateHolder(lockDir, sweepChildRunID); err != nil {
		t.Fatalf("AnnotateHolder: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	stalled, err := boxslot.SweepStalled(lockDir, runsDir, time.Millisecond)

	if err != nil {
		t.Fatalf("SweepStalled: %v", err)
	}
	if len(stalled) != 1 {
		t.Fatalf("stalled = %+v, want one row", stalled)
	}
	if !strings.Contains(stalled[0].Evidence, "missing") {
		t.Errorf("evidence %q missing the absent-envelope reason", stalled[0].Evidence)
	}
}

func TestSweepStalled_DeadHolderIsSkipped(t *testing.T) {
	lockDir, runsDir := t.TempDir(), t.TempDir()
	name := "holder-pid99999-1700000000000000000-1.lock"
	if err := os.WriteFile(filepath.Join(lockDir, name), []byte("pid=99999 start=2026-01-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatalf("seed stale holder: %v", err)
	}

	stalled, err := boxslot.SweepStalled(lockDir, runsDir, time.Millisecond)

	if err != nil {
		t.Fatalf("SweepStalled: %v", err)
	}
	if len(stalled) != 0 {
		t.Fatalf("dead holder reported stalled: %+v (admission GC owns it)", stalled)
	}
}

func TestAcquire_WaitReportsStalledBlocker(t *testing.T) {
	lockDir, runsDir := t.TempDir(), t.TempDir()
	release, err := boxslot.Acquire(context.Background(), boxslot.Options{MaxSlots: 1, LockDir: lockDir})
	if err != nil {
		t.Fatalf("Acquire holder: %v", err)
	}
	defer release()
	time.Sleep(10 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got []boxslot.StalledHolder
	_, err = boxslot.Acquire(ctx, boxslot.Options{
		MaxSlots:     1,
		LockDir:      lockDir,
		RunsDir:      runsDir,
		StallTTL:     time.Millisecond,
		PollInterval: 20 * time.Millisecond,
		OnStalled: func(stalled []boxslot.StalledHolder) {
			got = stalled
			cancel()
		},
	})

	if err == nil {
		t.Fatal("second Acquire returned without the slot freeing; want ctx cancellation")
	}
	if len(got) != 1 || got[0].PID != os.Getpid() {
		t.Fatalf("OnStalled got %+v, want the blocking holder (pid %d)", got, os.Getpid())
	}
}

// startSweepChild re-execs the test binary as a slot-holding child in
// mode "obey-term" (default SIGTERM disposition) or "ignore-term"
// (SIGTERM ignored, so only SIGKILL clears it), and waits for READY.
func startSweepChild(t *testing.T, lockDir, mode string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestReapStalled_ChildProcessHarness$", "-test.v")
	cmd.Env = append(os.Environ(),
		"BOXSLOT_SWEEP_CHILD_LOCK_DIR="+lockDir,
		"BOXSLOT_SWEEP_CHILD_MODE="+mode,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	ready := make(chan struct{})
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := stdout.Read(buf)
			if n > 0 && strings.Contains(string(buf[:n]), "READY") {
				close(ready)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("child never signaled READY")
	}
	return cmd
}

// TestReapStalled_ChildProcessHarness is the child half of the reap
// tests: under the env vars it holds a slot, annotates the fixed run
// id, and parks forever. As a normal test run it does nothing.
func TestReapStalled_ChildProcessHarness(t *testing.T) {
	childDir := os.Getenv("BOXSLOT_SWEEP_CHILD_LOCK_DIR")
	if childDir == "" {
		t.Skip("parent harness; runs only as a re-exec'd child")
	}
	if os.Getenv("BOXSLOT_SWEEP_CHILD_MODE") == "ignore-term" {
		signal.Ignore(syscall.SIGTERM)
	}
	release, err := boxslot.Acquire(context.Background(), boxslot.Options{MaxSlots: 1, LockDir: childDir})
	if err != nil {
		t.Fatalf("child: Acquire: %v", err)
	}
	if err := boxslot.AnnotateHolder(childDir, sweepChildRunID); err != nil {
		t.Fatalf("child: AnnotateHolder: %v", err)
	}
	_, _ = os.Stdout.WriteString("READY\n")
	_ = release
	select {}
}

// sweepChildStalled starts a child holder, backdates its envelope, and
// returns the child's stalled-holder descriptor.
func sweepChildStalled(t *testing.T, lockDir, runsDir, mode string) (*exec.Cmd, boxslot.StalledHolder) {
	t.Helper()
	cmd := startSweepChild(t, lockDir, mode)
	old := time.Now().Add(-2 * time.Hour)
	seedEnvelope(t, runsDir, sweepChildRunID, old)
	stalled, err := boxslot.SweepStalled(lockDir, runsDir, 30*time.Minute)
	if err != nil {
		t.Fatalf("SweepStalled: %v", err)
	}
	if len(stalled) != 1 || stalled[0].PID != cmd.Process.Pid {
		t.Fatalf("stalled = %+v, want one row for child pid %d", stalled, cmd.Process.Pid)
	}
	return cmd, stalled[0]
}

func TestReapStalled_EscalatesToSIGKILLAndAdmissionClearsMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal escalation semantics differ on windows")
	}
	lockDir, runsDir := t.TempDir(), t.TempDir()
	cmd, s := sweepChildStalled(t, lockDir, runsDir, "ignore-term")

	if err := boxslot.ReapStalled(s, 500*time.Millisecond); err != nil {
		t.Fatalf("ReapStalled: %v", err)
	}
	_ = cmd.Wait()

	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("reap removed the marker itself: %v (admission GC owns removal)", err)
	}
	release, err := boxslot.Acquire(context.Background(), boxslot.Options{MaxSlots: 1, LockDir: lockDir, NoWait: true})
	if err != nil {
		t.Fatalf("Acquire after reap: %v", err)
	}
	defer release()
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatalf("admission did not clear the dead child's marker: %v", err)
	}
}

func TestReapStalled_TermObeyingChildExitsBeforeGrace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal escalation semantics differ on windows")
	}
	lockDir, runsDir := t.TempDir(), t.TempDir()
	cmd, s := sweepChildStalled(t, lockDir, runsDir, "obey-term")

	start := time.Now()
	if err := boxslot.ReapStalled(s, 10*time.Second); err != nil {
		t.Fatalf("ReapStalled: %v", err)
	}
	_ = cmd.Wait()

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("reap took %s; a SIGTERM-obeying child must clear well before the grace window", elapsed)
	}
}

func TestReapStalled_RefusesWhenMarkerRenamed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal escalation semantics differ on windows")
	}
	lockDir, runsDir := t.TempDir(), t.TempDir()
	cmd, s := sweepChildStalled(t, lockDir, runsDir, "obey-term")

	moved := s.Path + ".moved"
	if err := os.Rename(s.Path, moved); err != nil {
		t.Fatalf("rename marker: %v", err)
	}

	err := boxslot.ReapStalled(s, 200*time.Millisecond)

	if err == nil {
		t.Fatal("reap of a vanished marker succeeded; want refusal")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("refusal error %q does not say it refuses", err)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("child no longer signalable after refused reap: %v (must not have been killed)", err)
	}
}

func TestReapStalled_RefusesWhenFlockReleasedBetweenSweepAndReap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal escalation semantics differ on windows")
	}
	lockDir, runsDir := t.TempDir(), t.TempDir()
	cmd, s := sweepChildStalled(t, lockDir, runsDir, "obey-term")

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child between sweep and reap: %v", err)
	}
	_ = cmd.Wait()

	err := boxslot.ReapStalled(s, 200*time.Millisecond)

	if !errors.Is(err, boxslot.ErrHolderReleased) {
		t.Fatalf("reap after the owner released its flock = %v, want ErrHolderReleased before any signal", err)
	}
	if _, statErr := os.Stat(s.Path); statErr != nil {
		t.Fatalf("marker vanished (%v); the refusal must come from the released flock, not a missing file", statErr)
	}
}
