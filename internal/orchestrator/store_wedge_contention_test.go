//go:build !windows

package orchestrator

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const walReadMarkOffset = 123

const walReadMarkCount = 5

func TestStoreWedgeContention_ChildProcessHarness(t *testing.T) {
	shmPath := os.Getenv("SPARKWING_WEDGE_CHILD_SHM")
	if shmPath == "" {
		t.Skip("parent harness; runs only as a re-exec'd child")
	}
	f, err := os.OpenFile(shmPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("child: open shm: %v", err)
	}
	lock := syscall.Flock_t{
		Type:   syscall.F_WRLCK,
		Whence: 0,
		Start:  walReadMarkOffset,
		Len:    walReadMarkCount,
	}
	retry := time.NewTicker(10 * time.Millisecond)
	timeout := time.NewTimer(2 * time.Second)
	for {
		err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock)
		if err == nil {
			retry.Stop()
			timeout.Stop()
			break
		}
		select {
		case <-retry.C:
		case <-timeout.C:
			retry.Stop()
			t.Fatalf("child: lock read marks: %v", err)
		}
	}
	_, _ = os.Stdout.WriteString("LOCKED\n")
	select {}
}

func startShmLockChild(t *testing.T, shmPath string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestStoreWedgeContention_ChildProcessHarness$", "-test.v")
	cmd.Env = append(os.Environ(), "SPARKWING_WEDGE_CHILD_SHM="+shmPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	locked := make(chan struct{})
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := stdout.Read(buf)
			if n > 0 && strings.Contains(string(buf[:n]), "LOCKED") {
				close(locked)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-locked:
	case <-time.After(10 * time.Second):
		t.Fatal("child never signaled LOCKED")
	}
}

func TestStoreWedgeGuard_TerminalOnRealWALShmContention(t *testing.T) {
	t.Setenv(StoreWedgeBudgetEnvVar, "5s")
	t.Setenv(store.BusyTimeoutEnvVar, "2000")

	dbPath := filepath.Join(t.TempDir(), "state.db")
	shmAnchor, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	defer shmAnchor.Close()
	shmPath := dbPath + "-shm"
	if _, err := os.Stat(shmPath); err != nil {
		t.Fatalf("no -shm beside the WAL-mode store: %v", err)
	}
	startShmLockChild(t, shmPath)

	var events bytes.Buffer
	guard, err := newStoreWedgeGuardFromEnv()
	if err != nil {
		t.Fatalf("newStoreWedgeGuardFromEnv: %v", err)
	}
	guard.logger = slog.New(slog.NewTextHandler(&events, nil))

	retry := time.NewTicker(100 * time.Millisecond)
	defer retry.Stop()
	deadlineAt := time.Now().Add(60 * time.Second)
	deadline := time.NewTimer(time.Until(deadlineAt))
	defer deadline.Stop()
	var terminal, lastStoreErr error
	timedOut := false
	for terminal == nil && !timedOut {
		if !time.Now().Before(deadlineAt) {
			timedOut = true
			break
		}
		st, err := store.Open(dbPath)
		if err == nil {
			_ = st.Close()
			guard.success()
		} else {
			lastStoreErr = err
			terminal = guard.fail("contention repro open", err)
		}
		if terminal != nil {
			continue
		}
		select {
		case <-retry.C:
		case <-deadline.C:
			timedOut = true
		}
	}

	if terminal == nil {
		t.Fatalf("wedge guard never went terminal within 60s of real shm contention (last store error: %v)", lastStoreErr)
	}
	t.Logf("driver error under real shm contention: %v", lastStoreErr)
	protocolFlavored := store.IsProtocolErr(lastStoreErr)
	budgetTripped := strings.Contains(terminal.Error(), "looks wedged")
	if !protocolFlavored && !budgetTripped {
		t.Fatalf("terminal verdict is neither protocol-classified nor a budget trip\n  store error: %v\n  terminal: %v", lastStoreErr, terminal)
	}
	if protocolFlavored && !strings.Contains(strings.ToLower(lastStoreErr.Error()), "locking protocol") {
		t.Errorf("IsProtocolErr matched %q without the driver's stable \"locking protocol\" text", lastStoreErr)
	}

	got := events.String()
	if !strings.Contains(got, `msg="store wedged"`) {
		t.Errorf("no structured wedge event emitted; events: %q", got)
	}
	if !strings.Contains(got, "kind=protocol") && !strings.Contains(got, "kind=budget") {
		t.Errorf("wedge event missing its kind field; events: %q", got)
	}
}
