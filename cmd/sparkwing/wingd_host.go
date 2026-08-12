// Daemon-host handoff for `sparkwing run`.
//
// The installed Sparkwing distribution owns daemon lifecycle. Pipeline
// clients declare required capabilities and use the running daemon; they
// never host, replace, or upgrade it. This file is the CLI's half of
// that: before it execs a compiled pipeline binary it names itself as the
// binary to spawn for the daemon, and -- when the invocation will
// actually admit work -- brings the daemon to its own version first, the
// duty pipeline binaries no longer carry.
package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

// ensureDaemonTimeout bounds the pre-exec readiness step. It is generous
// enough to cover a cold daemon binding its socket, and short enough that
// a machine where that never happens still hands off to the pipeline
// binary promptly -- which then reports the real fault, with the plan in
// hand to know whether it is fatal.
const ensureDaemonTimeout = 10 * time.Second

// withWingdHost points the pipeline binary's admission client at this CLI
// as the daemon host by setting $SPARKWING_WINGD_BIN. An operator's own
// exported value wins: the variable is documented as settable -- it is
// how a directly-invoked pipeline binary is pointed at a sparkwing that
// is not on PATH -- so `sparkwing run` must not silently overwrite it.
// Resolution failure leaves the env unset and the client falls back to
// the `sparkwing` on PATH.
func withWingdHost(env []string) []string {
	if os.Getenv(wingdclient.HostBinEnv) != "" {
		return env
	}
	self, err := os.Executable()
	if err != nil {
		return env
	}
	return setEnv(env, wingdclient.HostBinEnv, self)
}

// runNeedsDaemon is the whole gate on the pre-exec readiness step: this
// invocation must be one that admits work, and not a dry run. A dry run
// still submits an admission request, but it mutates nothing and finishes
// in seconds, so whatever the box already offers is good enough for it --
// and it is exactly the invocation an operator reaches for on a machine
// they are not sure about.
func runNeedsDaemon(wf runFlags, passthrough []string) bool {
	return !wf.dryRun && runInvokesAdmission(passthrough)
}

// runInvokesAdmission reports whether this `sparkwing run` invocation will
// actually admit work once the pipeline binary takes over. The inspection
// passthroughs -- `--explain`, `--plan`, `--help`, and the `config`
// subcommand -- return inside the pipeline binary before any admission
// happens, and must not pay for daemon readiness on their way through:
// bringing a daemon up costs a process, and replacing one drains it,
// disconnecting every current holder into a reconnect/reattach cycle.
// Neither is a price a documentation command may charge.
func runInvokesAdmission(passthrough []string) bool {
	if len(passthrough) > 0 && passthrough[0] == "config" {
		return false
	}
	for _, arg := range passthrough {
		switch arg {
		case "--explain", "--plan", "-h", "--help":
			return false
		}
	}
	return true
}

// ensureRunDaemon brings this machine's admission daemon to a state the
// run about to be exec'd can use: it starts one when none is running and
// replaces one this CLI's build supersedes, both at this CLI's version.
// Pipeline-binary clients do neither, so without this a stale daemon left
// by an older CLI would keep serving until it idled out, and a declaring
// run on a machine with no daemon would depend on its own lazy bring-up.
//
// Best-effort throughout. An error here must never block the run: the
// pipeline binary's own client will meet the same daemon with the plan in
// hand, and only it can tell whether the failure is fatal for this
// pipeline. EnsureDaemon's supersedes rules already refuse the
// replacements that could loop (dev vs release, identical builds, daemon
// newer), and its takeover budget bounds the rest.
func ensureRunDaemon() {
	sock, err := wingd.SocketPath("")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ensureDaemonTimeout)
	defer cancel()

	info, perr := wingdclient.Probe(ctx, sock)
	switch {
	case perr == nil && info.Draining:
		// A draining daemon is already being replaced and is not
		// upgradable; its successor is on the way. Connecting here would
		// only wait out the drain, which the run's own client does anyway
		// when it needs a lease.
		return
	case perr != nil && !errors.Is(perr, wingdclient.ErrNoDaemon):
		// Only a clean absence counts as "not running". Any other probe
		// failure means we could not look, and acting blind risks spawning
		// a rival for a daemon that is arbitrating other runs behind a
		// socket we failed to reach.
		return
	}

	cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Version: installedVersion()})
	if err != nil {
		return
	}
	_ = cl.Close()
}
