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

// walReadMarkOffset is the file offset of the first WAL read-mark
// lock byte in a SQLite -shm file: the lock range starts at offset
// 120 (WRITE), and the five aReadMark bytes sit at offsets 123-127.
const walReadMarkOffset = 123

// walReadMarkCount is how many aReadMark lock bytes a WAL shm file
// carries.
const walReadMarkCount = 5

// TestStoreWedgeContention_ChildProcessHarness is the child half of
// the real-contention wedge test: under the env var it takes fcntl
// write-locks on the WAL read-mark bytes of the named -shm file,
// prints LOCKED, and parks until killed -- the antagonist process
// that starves every reader in another process. As a normal test run
// it does nothing.
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
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child: lock read marks: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, _ = os.Stdout.WriteString("LOCKED\n")
	select {}
}

// startShmLockChild re-execs the test binary as the antagonist that
// write-locks shmPath's WAL read marks, and waits for its LOCKED
// handshake. The fcntl locks die with the child, which t.Cleanup
// kills.
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

// TestStoreWedgeGuard_TerminalOnRealWALShmContention reproduces the
// production wedge against the real driver, no injected seams: a
// second process holding fcntl write-locks on the -shm read-mark
// bytes starves every reader in this process, and the orchestrator
// poll shape -- one store call per tick, each outcome fed to the
// wedge guard -- must go terminal well inside the hard deadline
// instead of spinning forever. Observed from modernc.org/sqlite
// v1.50.0 under this contention: "locking protocol (15)" (surfaced
// after the driver's ~10s WAL_RETRY ladder), which
// store.IsProtocolErr classifies unmodified; the assertions below pin
// that wording so a driver reword goes red here instead of the
// classifier silently never matching.
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

	deadline := time.Now().Add(60 * time.Second)
	var terminal, lastStoreErr error
	for time.Now().Before(deadline) {
		st, err := store.Open(dbPath)
		if err == nil {
			_ = st.Close()
			guard.success()
			time.Sleep(100 * time.Millisecond)
			continue
		}
		lastStoreErr = err
		if terminal = guard.fail("contention repro open", err); terminal != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
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
