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

// EOF on the pipe is the dispatcher's death. The node has to hear it
// even though no signal was sent.
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

// A step body that ignores its context survives the cancel. The grace
// exists so such a node still stops instead of holding the machine for
// a run nobody owns. (In a real node a write to the dead dispatcher's
// stdout usually raises SIGPIPE first, but a node that writes nothing
// would never trip that, and this is the case it covers.)
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

// A node that stops on its own during the grace exits through its own
// path; the guard must not race it to a different status.
func TestWatchLiveness_NodeThatStopsInTimeIsNotKilled(t *testing.T) {
	r, closeWrite := livenessPipe(t)
	var exits atomic.Int64
	stop := watchLiveness(r, func() {}, 2*time.Second, func(int) { exits.Add(1) })

	closeWrite()
	time.Sleep(100 * time.Millisecond)
	stop() // safety: the node finished and the CLI is unwinding
	time.Sleep(300 * time.Millisecond)

	if got := exits.Load(); got != 0 {
		t.Fatalf("the guard exited %d time(s) over a node that had already stopped", got)
	}
}

// Without the dispatcher's say-so the descriptor is not ours. This
// very test binary proves why that matters: `go test` hands it a pipe
// on fd 3, so a probe that accepted any pipe would claim the
// harness's descriptor and then read and close it.
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
