package orchestrator

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runners/local"
)

// WatchParentLiveness cancels the node when the dispatcher that
// spawned it is gone.
//
// The dispatcher passes one end of a pipe it never writes to and
// holds the other for the life of the run. Reading that descriptor
// therefore blocks until the dispatcher's process dies, at which
// point the kernel closes its end and the read returns EOF. A signal
// handler cannot cover this: a dispatcher killed with SIGKILL sends
// nothing, and without the pipe its node processes would keep running
// against a run nobody is coordinating any more.
//
// Cancellation alone is not enough to guarantee the node stops. A
// step body that ignores its context -- a bare exec.Command, a
// blocking network call, a loop that never checks ctx.Err() -- would
// survive the cancel and keep holding the machine's resources for a
// run nobody owns. So the EOF starts a bounded grace period, and a
// node still alive at the end of it exits hard.
//
// In practice a node usually dies sooner than the grace and by a
// different route: its stdout and stderr were the dispatcher's pipes,
// so the first write after the dispatcher's death raises SIGPIPE. That
// is a side effect, not a guarantee -- a node that writes nothing
// would never trip it -- which is precisely what the grace covers.
//
// Returns a stop function. A descriptor that is not a readable pipe
// (a child started without one, or a platform that cannot pass one)
// yields a no-op watcher rather than an error: the pipe is a safety
// net, not a precondition for running a node.
func WatchParentLiveness(onGone func()) func() {
	f := openParentLivenessPipe()
	if f == nil {
		return func() {}
	}
	return watchLiveness(f, onGone, orphanExitGrace, exitProcess)
}

// orphanExitGrace is how long a node gets to unwind after its
// dispatcher dies. Long enough for a cooperative step to notice the
// cancelled context and for the node's terminal row to be written,
// short enough that an abandoned process is not still running when an
// operator next looks.
const orphanExitGrace = 5 * time.Second

// OrphanExitCode is what a node process exits with when its
// dispatcher died and it did not stop on its own. It is distinct so
// the status is diagnosable after the fact: nothing else in this
// program exits with it, and no signal maps to it.
const OrphanExitCode = 97

// openParentLivenessPipe claims the descriptor the dispatcher passed,
// or nil when this process was not given one.
//
// The environment variable, not the descriptor's shape, is what grants
// the claim. Probing fd 3 and accepting it because it looks like a
// pipe is a false positive waiting to happen -- `go test` hands its
// binary a pipe on exactly that descriptor -- and acting on it means
// reading and closing something another subsystem owns.
func openParentLivenessPipe() *os.File {
	raw := strings.TrimSpace(os.Getenv(local.ParentLivenessFDEnv))
	if raw == "" {
		return nil
	}
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < local.ParentLivenessFD {
		slog.Default().Warn("ignoring malformed parent-liveness descriptor",
			"env", local.ParentLivenessFDEnv, "value", raw)
		return nil
	}
	f := os.NewFile(uintptr(fd), "sparkwing-parent-liveness")
	if f == nil {
		return nil
	}
	info, statErr := f.Stat()
	if statErr != nil || info.Mode()&os.ModeNamedPipe == 0 {
		// safety: not closed. The dispatcher named this descriptor, but it is
		// not the pipe it should be, so the safe reading is that something
		// else now owns it.
		slog.Default().Warn("parent-liveness descriptor is not a pipe; orphan detection is off", "fd", fd)
		return nil
	}
	return f
}

// watchLiveness is WatchParentLiveness over an already-open pipe, with
// the process exit injected so the grace behavior can be tested
// without ending the test binary.
func watchLiveness(f *os.File, onGone func(), grace time.Duration, exit func(int)) func() {
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		for {
			_, err := f.Read(buf)
			if err == nil {
				continue // safety: the dispatcher writes nothing; ignore anything that arrives
			}
			if err != io.EOF {
				slog.Default().Debug("parent liveness watch ended", "err", err)
			}
			select {
			case <-done:
			default:
				slog.Default().Warn("dispatcher process is gone; abandoning node",
					"grace", grace, "exit_code_if_unresponsive", OrphanExitCode)
				onGone()
				exitIfStillAlive(done, grace, exit)
			}
			return
		}
	}()
	return func() {
		close(done)
		_ = f.Close()
	}
}

// exitIfStillAlive ends the process when cancellation did not. It
// waits for the node to finish on its own first -- stop() closing done
// is that signal -- so a step that honors its context still gets to
// write the node's terminal row.
func exitIfStillAlive(done <-chan struct{}, grace time.Duration, exit func(int)) {
	select {
	case <-done:
		return
	case <-time.After(grace):
	}
	slog.Default().Error("node did not stop after its dispatcher died; exiting",
		"grace", grace, "exit_code", OrphanExitCode)
	exit(OrphanExitCode)
}

//nolint:forbidigo // the deferred cleanup this skips belongs to a run that no longer has an owner; the alternative is an orphan process outliving every operator's view of it
func exitProcess(code int) { os.Exit(code) }
