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

func WatchParentLiveness(onGone func()) func() {
	f := openParentLivenessPipe()
	if f == nil {
		return func() {}
	}
	return watchLiveness(f, onGone, orphanExitGrace, exitProcess)
}

const orphanExitGrace = 5 * time.Second

const OrphanExitCode = 97

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
