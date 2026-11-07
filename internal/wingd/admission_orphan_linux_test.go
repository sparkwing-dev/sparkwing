package wingd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestAdmissionOrphanHelper(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_ORPHAN_HELPER") != "1" {
		return
	}
	cmd := procenv.ControlCommand(nil, "/bin/sh", "-c", "cat <&3 & echo $!; wait")
	cmd.Stdin = os.Stdin
	cmd.ExtraFiles = []*os.File{os.Stdin}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	group, err := procgroup.StartSession(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var child int
	if _, err := fmt.Fscan(output, &child); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(os.Stdout, "%d %d\n", group.ID(), child); err != nil {
		t.Fatal(err)
	}
	if err := group.Finish(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
}

type orphanFixture struct {
	holder int
	root   int
	child  int
	birth  string
	stop   func()
}

func startAdmissionOrphan(t *testing.T) orphanFixture {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := procenv.ControlCommand(nil, binary, "-test.run=^TestAdmissionOrphanHelper$")
	cmd.Env = append(os.Environ(), "SPARKWING_TEST_ORPHAN_HELPER=1")
	cmd.Stdin = read
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	t.Cleanup(stop)
	var root, child int
	if _, err := fmt.Fscan(bufio.NewReader(output), &root, &child); err != nil {
		t.Fatal(err)
	}
	birth, err := procgroup.ProcessBirth(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := procgroup.KillSession(procgroup.SessionIdentity{LeaderPID: root, SessionID: root, BirthToken: birth}); err != nil {
			t.Error(err)
		}
	})
	return orphanFixture{holder: cmd.Process.Pid, root: root, child: child, birth: birth, stop: stop}
}

func registerAdmissionOrphan(t *testing.T, d *Daemon, job orphanFixture) AdmissionHTTPState {
	t.Helper()
	input := admissionTestInput(t, "orphan", 2)
	input.RootPID, input.Request.PID = job.holder, job.holder
	var err error
	input.ProcessBirth, err = procgroup.ProcessBirth(job.holder)
	if err != nil {
		t.Fatal(err)
	}
	held := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", input))
	admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests/"+held.ID+"/process", map[string]any{"pid": job.root, "process_birth": job.birth}))
	return held
}

func TestAdmissionOrphanCleanupSurvivesControllerRestart(t *testing.T) {
	job := startAdmissionOrphan(t)
	path := filepath.Join(t.TempDir(), "state.db")
	persistence, err := teststore.Open(t, path)
	if err != nil {
		t.Fatal(err)
	}
	d := newHTTPAdmissionTest(t, persistence)
	held := registerAdmissionOrphan(t, d, job)
	job.stop()
	if err := admissionProcessRunning(job.child); err != nil {
		t.Fatalf("job child did not survive runner death: %v", err)
	}
	if err := d.reapAdmission(); err != nil {
		t.Fatal(err)
	}
	if !d.admissionTickets[held.ID].Active {
		t.Fatal("orphan capacity was released before process cleanup")
	}
	if err := persistence.Close(); err != nil {
		t.Fatal(err)
	}
	persistence, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = persistence.Close() })
	restarted := newHTTPAdmissionTest(t, persistence)
	if err := restarted.cleanupAdmissionProcesses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := admissionProcessRunning(job.child); !errors.Is(err, procgroup.ErrProcessAbsent) {
		t.Fatalf("orphan child still running: %v", err)
	}
	if err := restarted.reapAdmission(); err != nil {
		t.Fatal(err)
	}
	ticket := restarted.admissionTickets[held.ID]
	if ticket.Active || ticket.MeasurementPID != 0 {
		t.Fatalf("cleaned orphan retained capacity or process identity: %+v", ticket)
	}
}

func TestAdmissionOrphanCleanupRetriesAfterFailedAcknowledgement(t *testing.T) {
	job := startAdmissionOrphan(t)
	persistence := &admissionMemoryStore{}
	d := newHTTPAdmissionTest(t, persistence)
	held := registerAdmissionOrphan(t, d, job)
	job.stop()
	persistence.fail = true
	if err := d.cleanupAdmissionProcesses(t.Context()); err == nil {
		t.Fatal("cleanup did not report failed persistence")
	}
	persistence.fail = false
	restarted := newHTTPAdmissionTest(t, persistence)
	if err := restarted.cleanupAdmissionProcesses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.reapAdmission(); err != nil {
		t.Fatal(err)
	}
	if restarted.admissionTickets[held.ID].Active {
		t.Fatal("retry did not release cleaned orphan")
	}
}

func TestAdmissionKeepsLiveCancelledRootsPastReceiptRetention(t *testing.T) {
	job := startAdmissionOrphan(t)
	persistence := &admissionMemoryStore{}
	d := newHTTPAdmissionTest(t, persistence)
	held := registerAdmissionOrphan(t, d, job)
	admissionResponse(t, admissionHTTPCall(t, d, http.MethodDelete, "/requests/"+held.ID, nil))
	d.mu.Lock()
	d.admissionTickets[held.ID].TerminalAt = d.now().Add(-2 * terminalAdmissionRetention)
	err := d.commitAdmissionLocked()
	d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.admissionTickets[held.ID]; !ok {
		t.Fatal("pruning forgot a live execution root")
	}
	job.stop()
	restarted := newHTTPAdmissionTest(t, persistence)
	if err := restarted.cleanupAdmissionProcesses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := admissionProcessRunning(job.child); !errors.Is(err, procgroup.ErrProcessAbsent) {
		t.Fatalf("released ticket lost orphan cleanup: %v", err)
	}
}

func TestAdmissionCleanupLeavesLiveHolderAndReusedRootAlone(t *testing.T) {
	job := startAdmissionOrphan(t)
	d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
	held := registerAdmissionOrphan(t, d, job)
	if err := d.cleanupAdmissionProcesses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := admissionProcessRunning(job.child); err != nil {
		t.Fatalf("cleanup killed a live holder's job: %v", err)
	}
	job.stop()
	d.mu.Lock()
	d.admissionTickets[held.ID].MeasurementBirth = "reused-" + strconv.Itoa(job.root)
	err := d.commitAdmissionLocked()
	d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.cleanupAdmissionProcesses(t.Context()); err == nil {
		t.Fatal("PID reuse with live session members was not refused")
	}
	if err := admissionProcessRunning(job.child); err != nil {
		t.Fatalf("cleanup signaled a reused root: %v", err)
	}
}

func TestAdmissionKeepsCancelledTreeChargedUntilHostRelease(t *testing.T) {
	job := startAdmissionOrphan(t)
	d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
	held := registerAdmissionOrphan(t, d, job)
	waiting := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "waiting", 2)))
	if err := d.cancelCommittedRunAdmission(t.Context(), "orphan"); err != nil {
		t.Fatal(err)
	}
	cancelled := admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+held.ID+"/watch", nil))
	if cancelled.Cancel == nil {
		t.Fatalf("cancellation did not reach lease watch: %+v", cancelled)
	}
	state := admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+waiting.ID, nil))
	if state.Queued == nil {
		t.Fatalf("cancellation freed capacity before job exit: %+v", state)
	}
	if err := procgroup.KillSession(procgroup.SessionIdentity{LeaderPID: job.root, SessionID: job.root, BirthToken: job.birth}); err != nil {
		t.Fatal(err)
	}
	admissionResponse(t, admissionHTTPCall(t, d, http.MethodDelete, "/requests/"+held.ID, nil))
	state = admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+waiting.ID, nil))
	if state.Grant == nil {
		t.Fatalf("host release did not promote waiter: %+v", state)
	}
}

func TestAdmissionCleanupStopsWithControllerContext(t *testing.T) {
	job := startAdmissionOrphan(t)
	d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
	held := registerAdmissionOrphan(t, d, job)
	job.stop()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := d.cleanupAdmissionProcesses(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cleanup: %v", err)
	}
	if err := admissionProcessRunning(job.child); err != nil {
		t.Fatalf("cancelled cleanup killed the job: %v", err)
	}
	if !d.admissionTickets[held.ID].Active || d.admissionTickets[held.ID].MeasurementPID != job.root {
		t.Fatal("cancelled cleanup forgot the job or its reservation")
	}
}

func TestAdmissionReleasesCancelledTreeWhenHostReleaseIsLost(t *testing.T) {
	job := startAdmissionOrphan(t)
	d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
	held := registerAdmissionOrphan(t, d, job)
	waiting := admissionResponse(t, admissionHTTPCall(t, d, http.MethodPost, "/requests", admissionTestInput(t, "waiting", 2)))
	if err := d.cancelCommittedRunAdmission(t.Context(), "orphan"); err != nil {
		t.Fatal(err)
	}
	if err := d.cleanupAdmissionProcesses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !d.admissionTickets[held.ID].Active {
		t.Fatal("cancellation released capacity while the job still ran")
	}
	if err := procgroup.KillSession(procgroup.SessionIdentity{LeaderPID: job.root, SessionID: job.root, BirthToken: job.birth}); err != nil {
		t.Fatal(err)
	}
	if err := d.cleanupAdmissionProcesses(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ticket := d.admissionTickets[held.ID]; ticket.Active || ticket.MeasurementPID != 0 {
		t.Fatalf("live holder kept a cancelled, exited job's capacity: %+v", ticket)
	}
	if err := admissionProcessRunning(job.holder); err != nil {
		t.Fatalf("holder should outlive its job: %v", err)
	}
	state := admissionResponse(t, admissionHTTPCall(t, d, http.MethodGet, "/requests/"+waiting.ID, nil))
	if state.Grant == nil {
		t.Fatalf("released capacity did not promote waiter: %+v", state)
	}
}
