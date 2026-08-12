// Daemon hosting for clients that are not themselves daemon hosts.
//
// The invariant: the installed Sparkwing distribution owns daemon
// lifecycle. Pipeline clients declare required capabilities and use the
// running daemon; they never host, replace, or upgrade it. A compiled
// pipeline binary therefore does not serve the `wingd` verbs and must
// not re-exec itself to bring a daemon up. The one spawn it retains
// starts an *installed* binary -- the one named by [HostBinEnv], else a
// `sparkwing` on PATH -- so a repo's .sparkwing/go.mod pin never becomes
// the machine's daemon version.
package client

import (
	"errors"
	"os"
	"os/exec"
)

// HostBinEnv names the installed binary a non-hosting client spawns to
// serve the daemon. The sparkwing CLI sets it to its own executable
// before exec'ing a compiled pipeline binary; an operator may export it
// to point a directly-invoked pipeline binary (systemd unit, deploy box)
// at a sparkwing that is not on PATH.
const HostBinEnv = "SPARKWING_WINGD_BIN"

// ErrNoDaemonHost reports that no daemon is running and this process has
// no way to start one: it does not serve the `wingd` verbs itself, and
// no host binary was resolvable via [HostBinEnv] or a `sparkwing` on
// PATH.
//
// It is deliberately distinct from [ErrDaemonUnreachable]. The connect
// loop only reaches this sentinel after a dial failure that means
// nothing is listening (ENOENT or ECONNREFUSED); a socket that could not
// be reached at all still reports unreachable, because a caller deciding
// whether to run without coordination must not treat "I could not look"
// like "the box is idle".
var ErrNoDaemonHost = errors.New("wingd/client: no admission daemon is running and no sparkwing binary is available to host one")

// ResolveHostBin returns the daemon-host binary a non-hosting client
// should spawn: $SPARKWING_WINGD_BIN when set, else a `sparkwing` found
// on PATH. ok is false when neither resolves.
func ResolveHostBin() (bin string, ok bool) {
	if bin := os.Getenv(HostBinEnv); bin != "" {
		return bin, true
	}
	if bin, err := exec.LookPath("sparkwing"); err == nil {
		return bin, true
	}
	return "", false
}

// HostSpawn returns the Spawn for a client that cannot host the daemon
// itself: it starts the resolved installed binary as a detached `wingd
// supervise`. ok is false when no host binary resolves, and the caller
// should pass [NoHostSpawn] so the connect loop reports the absence with
// its own sentinel instead of falling back to re-execing this binary.
//
// The spawn deliberately drops the client's version: the host binary
// advertises its own build, so the daemon's version tracks the installed
// sparkwing rather than whichever SDK pin happened to spawn it.
func HostSpawn() (spawn func(home, version string) error, ok bool) {
	bin, ok := ResolveHostBin()
	if !ok {
		return nil, false
	}
	return func(home, _ string) error {
		return spawnDetached(bin, home, "")
	}, true
}

// NoHostSpawn is the Spawn for a non-hosting client on a machine with no
// installed sparkwing. It starts nothing and reports [ErrNoDaemonHost],
// which callers that can run without local coordination treat as "no
// admission" rather than as a failure.
func NoHostSpawn(string, string) error { return ErrNoDaemonHost }
