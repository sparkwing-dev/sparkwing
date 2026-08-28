package orchestrator

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runners/local"
)

func livenessPipe(t *testing.T) (read *os.File, closeWrite func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return r, func() { _ = w.Close() }
}

func TestWatchLiveness_EOFCancelsTheNode(t *testing.T) {
	r, closeWrite := livenessPipe(t)
	gone := make(chan struct{})
	stop := watchLiveness(r, func() { close(gone) }, time.Hour, func(int) {
		t.Error("exited while the node was still being given its grace")
	})
	defer stop()

	closeWrite()
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the node was never told its dispatcher had died")
	}
}

func TestWatchLiveness_ExitsWhenCancelIsIgnored(t *testing.T) {
	r, closeWrite := livenessPipe(t)
	var code atomic.Int64
	exited := make(chan struct{})
	stop := watchLiveness(r, func() {}, 50*time.Millisecond, func(c int) {
		code.Store(int64(c))
		close(exited)
	})
	defer stop()

	closeWrite()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("an orphaned node that ignored cancellation was left running")
	}
	if got := code.Load(); got != OrphanExitCode {
		t.Errorf("exit code = %d, want %d", got, OrphanExitCode)
	}
}

func TestWatchLiveness_NodeThatStopsInTimeIsNotKilled(t *testing.T) {
	r, closeWrite := livenessPipe(t)
	var exits atomic.Int64
	stop := watchLiveness(r, func() {}, 2*time.Second, func(int) { exits.Add(1) })

	closeWrite()
	time.Sleep(100 * time.Millisecond)
	stop()
	time.Sleep(300 * time.Millisecond)

	if got := exits.Load(); got != 0 {
		t.Fatalf("the guard exited %d time(s) over a node that had already stopped", got)
	}
}

func TestOpenParentLivenessPipe_RefusesADescriptorNoDispatcherNamed(t *testing.T) {
	t.Setenv(local.ParentLivenessFDEnv, "")
	if f := openParentLivenessPipe(); f != nil {
		t.Fatal("claimed a descriptor in a process no dispatcher spawned")
	}
}

func TestOpenParentLivenessPipe_RefusesAMalformedDescriptor(t *testing.T) {
	for _, v := range []string{"nonsense", "0", "1", "2", "-1"} {
		t.Setenv(local.ParentLivenessFDEnv, v)
		if f := openParentLivenessPipe(); f != nil {
			t.Errorf("claimed a descriptor for %s=%q", local.ParentLivenessFDEnv, v)
		}
	}
}
