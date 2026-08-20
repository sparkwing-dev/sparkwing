package client

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// daemonLogTailLines is how many trailing daemon-log lines the client
// folds into an error when a spawned daemon dies before serving.
const daemonLogTailLines = 8

// DaemonSpawnVerb is the `wingd` subcommand every spawn in this package
// invokes on the binary it starts. It is exported so the binaries that
// host the daemon can pin, in their own tests, that they serve it.
//
// That pin is not ceremony. Moving this verb to `supervise` (965f77d4)
// broke every local run without a live daemon, because the spawn re-execs
// whichever binary the client lives in and compiled pipeline binaries did
// not serve it; the fix (2669c87e) moved it back. The verb only became
// safe again once spawning moved to installed binaries -- which serve
// both verbs -- and pipeline binaries stopped spawning themselves at all.
// A binary that can be spawned as a daemon host must serve this verb, and
// the test that says so is what keeps the two from drifting apart again.
const DaemonSpawnVerb = "supervise"

// defaultSpawn re-execs this binary as a detached `wingd supervise` for
// home. It is the right spawn only for a binary that serves the `wingd`
// verbs itself -- the installed sparkwing CLI and sparkwing-runner. A
// compiled pipeline binary does not, and passes [HostSpawn] instead; a
// client that declares it cannot host ([Options.NoTakeover]) never
// reaches this function at all.
func defaultSpawn(home, version string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}
	return spawnDetached(self, home, version)
}

// hostStartGrace bounds how long a spawn watches a freshly started host
// for an immediate death. It ends the moment the socket appears, so a
// healthy start pays only the dial-poll interval; the cap only applies to
// a host that neither binds nor exits, which the connect loop's own
// startup budget then covers.
const hostStartGrace = 2 * time.Second

// hostStartPoll is how often the watch looks for the socket.
const hostStartPoll = 20 * time.Millisecond

// ErrDaemonHostFailed reports that the daemon host binary was started and
// exited without serving. It is distinct from a spawn syscall failure
// (the binary ran) and from an unreachable daemon (nothing is arbitrating
// behind that socket), and it is the sentinel a caller keys off to report
// the host rather than the socket.
var ErrDaemonHostFailed = errors.New("wingd/client: the daemon host binary exited without serving")

// spawnDetached starts bin as a detached `wingd supervise` for home. The
// daemon's stdout and stderr go to a log file beside its socket.
//
// It waits briefly to see the host either bind its socket or die. Racing
// spawns stay safe because only a non-zero exit counts as failure: the
// daemon's flock election lets one win and the losers exit cleanly, and a
// clean exit inside the window is exactly what an election loss looks
// like. A non-zero exit is not -- it is a host that could not serve at
// all, most sharply a binary too old to know the verb it was handed --
// and reporting it here turns a thirty-second wait for a socket that will
// never appear into an immediate error carrying the reason.
func spawnDetached(bin, home, version string) error {
	args := []string{"wingd", DaemonSpawnVerb}
	if home != "" {
		args = append(args, "--home", home)
	}
	if version != "" {
		args = append(args, "--version", version)
	}

	logF, logExisted := openDaemonLog(home)

	cmd := exec.Command(bin, args...)
	cmd.Stdin = nil
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Env = os.Environ()
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		// safety: a log this spawn created and never wrote to would imply a
		// daemon ran here; leave the directory as it was found instead.
		discardUnusedDaemonLog(logF, home, logExisted)
		return fmt.Errorf("start daemon: %w", err)
	}
	if logF != nil {
		_ = logF.Close()
	}
	if err := watchHostStart(cmd, bin, home); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

// watchHostStart waits for the spawned host to bind its socket or exit,
// whichever comes first, and reports a non-zero exit as a host failure
// naming the binary and the tail of the log it wrote. It returns nil once
// the host looks healthy or the grace window ends, leaving the connect
// loop to decide how long to keep waiting for a host that is merely slow.
//
// "Bound its socket" is judged against the socket that was there before
// the spawn, not against mere existence. During a takeover the
// predecessor's socket is still in place, and treating it as the
// successor's readiness would stop the watch instantly and miss a
// successor that could not start at all -- the exact case this exists to
// catch. A daemon binds a fresh socket file, so a different one is proof;
// the same one is not.
func watchHostStart(cmd *exec.Cmd, bin, home string) error {
	sock, serr := wingd.SocketPath(home)
	if serr != nil {
		return nil
	}
	before, _ := os.Stat(sock)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(hostStartGrace)
	for {
		select {
		case werr := <-exited:
			var exit *exec.ExitError
			if werr == nil || !errors.As(werr, &exit) {
				// A clean exit is an election loss: another daemon already
				// serves this home, which is the outcome this spawn wanted.
				return nil
			}
			return hostExitedEarly(bin, home, exit)
		default:
		}
		if now, err := os.Stat(sock); err == nil && (before == nil || !os.SameFile(before, now)) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		time.Sleep(hostStartPoll)
	}
}

// hostExitedEarly names the binary that failed, because the operator
// chose it -- through $SPARKWING_WINGD_BIN or by what is on PATH -- and
// "the daemon did not start" is not actionable without knowing which
// binary was asked to be it. The log tail carries the host's own reason.
func hostExitedEarly(bin, home string, exit *exec.ExitError) error {
	if tail := daemonLogTail(home); tail != "" {
		path, _ := wingd.LogPath(home)
		return fmt.Errorf("%w: %s %s (exit %d); %s:\n%s",
			ErrDaemonHostFailed, bin, exit.String(), exit.ExitCode(), path, tail)
	}
	return fmt.Errorf("%w: %s %s (exit %d)", ErrDaemonHostFailed, bin, exit.String(), exit.ExitCode())
}

// discardUnusedDaemonLog removes a log file this spawn created but never
// handed to a process, so an empty d.log never implies a daemon ran.
func discardUnusedDaemonLog(logF *os.File, home string, existed bool) {
	if logF == nil {
		return
	}
	_ = logF.Close()
	if existed {
		return
	}
	if path, err := wingd.LogPath(home); err == nil {
		if fi, serr := os.Stat(path); serr == nil && fi.Size() == 0 {
			_ = os.Remove(path)
		}
	}
}

// openDaemonLog prepares the daemon's log file for a detached spawn: it
// creates the daemon directory (which the spawned daemon has not yet made
// when the client opens the file), rotates the log once if it has grown
// past the cap ([wingd.RotateLogOverCap], which the running daemon shares
// so the two rotations keep one shape), and opens it append-only. The
// spawned daemon's stdout and stderr are pointed at the returned file, so
// its operational log and any early crash both land at the documented
// path. Nil on failure leaves the daemon's output discarded rather than
// blocking the spawn.
//
// The file has to exist before the process starts, because it is the
// process's stdout. existed reports whether it was already there, so a
// spawn that never runs can put the directory back the way it found it
// rather than leave an empty log implying a daemon ran. A rotation does
// not change that answer: it empties the log in place, so the file the
// caller found is still the file that is there, and a predecessor still
// writing through its own descriptor must not have it unlinked out from
// under it.
func openDaemonLog(home string) (f *os.File, existed bool) {
	path, err := wingd.LogPath(home)
	if err != nil {
		return nil, false
	}
	if dir, derr := wingd.StateDir(home); derr == nil {
		_ = os.MkdirAll(dir, 0o700)
	} else {
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	_, _ = wingd.RotateLogOverCap(home)
	_, serr := os.Stat(path)
	existed = serr == nil
	f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, existed
	}
	return f, existed
}

// daemonLogTail returns the last few non-empty lines of home's daemon log,
// or "" when it is absent or empty. It lets the client attach a
// startup-death cause the daemon wrote to its own log.
func daemonLogTail(home string) string {
	path, err := wingd.LogPath(home)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > daemonLogTailLines {
		lines = lines[len(lines)-daemonLogTailLines:]
	}
	return strings.Join(lines, "\n")
}
