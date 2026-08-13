// Package client dials sparkwingd, spawning the daemon when none is
// running. A run process uses it to obtain an all-or-nothing admission
// lease that lives as long as the connection; the CLI's queue view uses
// it read-only. The library owns connection lifecycle, the version
// handshake, and the newer-client takeover that drains an older daemon
// and spawns its successor.
package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// ErrProtocolTooOld is returned when this client's protocol major is
// below the oldest the running daemon still serves. A daemon meets any
// client inside its served range on that client's own major, so this is
// the genuinely incompatible tail rather than any pin that merely lags
// the daemon. It cannot be resolved by takeover: the client binary is
// what must move.
var ErrProtocolTooOld = errors.New("wingd/client: daemon no longer serves this client's protocol")

// protocolTooOld explains a protocol major the client cannot speak, naming
// both sides and the lever that actually moves.
//
// The client here is the pipeline binary compiled from the calling repo's
// .sparkwing/go.mod, not the sparkwing CLI on PATH -- the CLI is not a
// party to this handshake, so advice to upgrade it is advice the operator
// can follow to no effect. The daemon is machine-wide and the first run to
// need one brings it up, so the repo that spawned it need not be the repo
// now being refused.
//
// The release to raise to is looked up from the daemon's major rather than
// assumed to be this build's own boundary: a daemon can speak a major whose
// first release was cut after this binary, and then the only release known
// to speak to it is the one the daemon is running.
func protocolTooOld(selfVersion string, ack wingwire.HelloAck) error {
	self := selfVersion
	if self == "" {
		self = "(unknown)"
	}
	daemon := ack.BinaryVersion
	if daemon == "" {
		daemon = "(unknown)"
	}
	raiseTo, known := wingwire.ReleasedProtocolFloors().MinVersionSpeaking(ack.ProtocolMajor)
	if !known {
		if v := ack.BinaryVersion; semver.IsValid(v) && semver.Prerelease(v) == "" {
			raiseTo = v
		}
	}
	pinAdvice := fmt.Sprintf("Raise this repo's .sparkwing/go.mod pin to %s or newer", raiseTo)
	if raiseTo == "" {
		pinAdvice = fmt.Sprintf("Raise this repo's .sparkwing/go.mod pin to a release speaking protocol %d", ack.ProtocolMajor)
	}
	return fmt.Errorf("%w: daemon speaks protocol %d (sparkwing %s), this pipeline binary speaks protocol %d (sparkwing %s). "+
		"%s and re-run, or set SPARKWING_HOME to run against a daemon of your own; "+
		"upgrading the sparkwing CLI does not affect this handshake",
		ErrProtocolTooOld, ack.ProtocolMajor, daemon, wingd.ProtocolMajor, self, pinAdvice)
}

// ErrDaemonTooOld is returned when the running daemon's protocol major is
// below what this client speaks and the client may not replace it
// ([Options.NoTakeover]). The lever is the daemon's own binary -- the
// installed sparkwing that hosts it -- not this client's build, which is
// what separates it from [ErrProtocolTooOld].
var ErrDaemonTooOld = errors.New("wingd/client: daemon protocol is older than this client")

// ErrBuildMismatch is returned when two different or unidentified builds
// claim the same protocol major. Major equality alone cannot prove that one
// side understands fields added by the other, because JSON ignores unknown
// fields within a major.
var ErrBuildMismatch = errors.New("wingd/client: daemon build differs from this client")

func buildMismatch(selfVersion string, ack wingwire.HelloAck) error {
	self := strings.TrimSpace(selfVersion)
	daemon := strings.TrimSpace(ack.BinaryVersion)
	if self == daemon || (ack.BuildIdentity != "" && ack.BuildIdentity == wingwire.BuildIdentity) || supersedes(daemon, self) {
		return nil
	}
	if self == "" {
		self = "(unknown)"
	}
	if daemon == "" {
		daemon = "(unknown)"
	}
	return fmt.Errorf("%w: this build is %s and the daemon is %s; the same protocol major does not prove same-build compatibility. Restart the daemon with this build or use a separate SPARKWING_HOME",
		ErrBuildMismatch, self, daemon)
}

// FirstHostingRelease is the first sparkwing release whose installed
// binary can host the daemon for a client that does not host its own:
// the release that serves [DaemonSpawnVerb] and ships the host handoff.
//
// It is a hand-maintained fact, not something the build can derive, so
// the release owner must keep it honest: this constant names the version
// this work ships in, and a slipped release renames it here. Getting it
// wrong sends an operator to install a version that will not fix their
// problem, which is worse than saying nothing.
//
// It exists because the protocol floor is not the whole answer. A
// v0.24.0 or v0.25.0 install speaks the current protocol and would clear
// [wingwire.ProtocolFloors.MinVersionSpeaking], but serves no supervise
// verb, so it cannot host. The advice has to name whichever bar is
// higher.
const FirstHostingRelease = "v0.27.0"

// minHostingRelease is the release an operator must install for this
// client to be able to use a daemon that binary hosts: the higher of the
// protocol floor for this client's major and the first release that can
// host at all.
func minHostingRelease() string {
	floor, known := wingwire.ReleasedProtocolFloors().MinVersionSpeaking(wingd.ProtocolMajor)
	if !known {
		return FirstHostingRelease
	}
	if semver.Compare(floor, FirstHostingRelease) > 0 {
		return floor
	}
	return FirstHostingRelease
}

// daemonTooOld explains a daemon protocol major below what this client
// speaks, for a client that may not take the daemon over. A pipeline
// binary gets here when its SDK pin crossed a protocol boundary the
// installed sparkwing hosting the daemon has not reached, so the advice
// moves that installation, not the repo's go.mod pin.
//
// It names a release rather than a protocol number because "install
// sparkwing X" is an instruction an operator can carry out, and it names
// the hosting bar as well as the protocol one because clearing only the
// protocol bar leaves them with a binary that still cannot host.
//
// It deliberately suggests no SPARKWING_HOME escape: a private home gets
// its own daemon from the same unusable installation, so the suggestion
// would send the reader in a circle.
func daemonTooOld(selfVersion string, ack wingwire.HelloAck) error {
	self := selfVersion
	if self == "" {
		self = "(unknown)"
	}
	daemon := ack.BinaryVersion
	if daemon == "" {
		daemon = "(unknown)"
	}
	return fmt.Errorf("%w: daemon speaks protocol %d (sparkwing %s), this pipeline binary speaks protocol %d (sparkwing %s). "+
		"Install sparkwing %s or newer on this host, then run `sparkwing daemon restart`",
		ErrDaemonTooOld, ack.ProtocolMajor, daemon, wingd.ProtocolMajor, self, minHostingRelease())
}

// servedDownLevel reports that the daemon answered on an older major than
// it natively speaks, which happens only when this client is the older
// side. Such a client must not take the daemon over: replacing a newer
// daemon with an older one is a downgrade, and the successor would be
// taken over again by the next native client, with nothing bounding the
// exchange. Daemons predating NativeProtocolMajor report zero and are
// never treated as down-level.
func servedDownLevel(ack wingwire.HelloAck) bool {
	return ack.NativeProtocolMajor > ack.ProtocolMajor
}

// ErrReattachRejected is returned by [Client.Reattach] when the grace
// window has closed or the token is unknown; the caller should submit a
// fresh admission request instead.
var ErrReattachRejected = errors.New("wingd/client: re-attach rejected; lease is gone")

// Options configures how a client finds or starts its daemon.
type Options struct {
	// Home is the sparkwing home whose daemon to reach. Empty resolves the
	// default ($SPARKWING_HOME or ~/.sparkwing).
	Home string
	// Version is this binary's version, sent in the handshake and used to
	// decide whether to take over a daemon this build supersedes. Empty
	// never triggers takeover.
	Version string
	// Spawn starts a detached daemon for Home. Nil uses the default, which
	// re-execs this binary as `sparkwing wingd supervise`; a binary that
	// does not serve the `wingd` verbs itself passes [HostSpawn] (or
	// [NoHostSpawn]) instead.
	Spawn func(home, version string) error
	// NoTakeover shares a running daemon this build supersedes instead of
	// draining and replacing it, and forbids the self-exec default spawn.
	// It is set by clients that cannot host the daemon themselves --
	// compiled pipeline binaries -- for which "replace the daemon with my
	// own build" is not an action they can take: their build is not
	// installed anywhere the successor could come from, and routing the
	// replacement through the host binary would either be a no-op or let
	// two pins drain each other's daemon in a loop.
	//
	// Such a client never spends takeover budget, because it never takes
	// anything over. When the daemon's protocol major is below what this
	// client speaks, connect fails with [ErrDaemonTooOld] naming the
	// installed sparkwing as the lever, rather than attempting a
	// replacement it cannot perform.
	NoTakeover bool
	// DialTimeout bounds a single connect attempt. Zero uses a small
	// default.
	DialTimeout time.Duration
	// Backoff is the base wait between spawn-and-retry attempts. Zero uses
	// a small default.
	Backoff time.Duration
	// PredecessorWaitTimeout bounds how long a client waits for an unreachable
	// daemon to release this home's election. Zero uses the daemon startup window.
	PredecessorWaitTimeout time.Duration
	// Logf receives one-line diagnostics. Nil discards them.
	Logf func(format string, args ...any)
}

func (o Options) dialTimeout() time.Duration {
	if o.DialTimeout > 0 {
		return o.DialTimeout
	}
	return 500 * time.Millisecond
}

func (o Options) backoff() time.Duration {
	if o.Backoff > 0 {
		return o.Backoff
	}
	return defaultBackoff
}

func (o Options) predecessorWaitTimeout() time.Duration {
	if o.PredecessorWaitTimeout > 0 {
		return o.PredecessorWaitTimeout
	}
	return time.Duration(dialsPerSpawn) * defaultBackoff
}

func (o Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

func (o Options) spawn(home, version string) error {
	if o.Spawn != nil {
		return o.Spawn(home, version)
	}
	// safety: the self-exec default is only correct for a binary that serves
	// the `wingd` verbs. A NoTakeover client is by definition not one, so an
	// unset Spawn there is a wiring mistake, and re-execing anyway would put
	// the very binary this feature keeps out of the daemon role into it.
	if o.NoTakeover {
		return ErrNoDaemonHost
	}
	return defaultSpawn(home, version)
}

// Client is a live, handshaked connection to a daemon. It retains the
// options and socket it was opened with so a frame-read that fails on a
// daemon blink (kill, idle-exit, or version takeover) can transparently
// reconnect and reattach within the daemon's grace window instead of
// surfacing a bare closed-connection error to the run.
type Client struct {
	nc   net.Conn
	dec  *frameReader
	ack  wingwire.HelloAck
	opts Options
	sock string
	// closed marks an intentional Close so a frame-read failure that follows
	// it is not mistaken for a daemon blink and does not trigger a reconnect.
	closed atomic.Bool
	// probe declares this client a health probe in its hello, which keeps
	// the connection out of the daemon's idle accounting. Only [Probe] and
	// [HealthProbe] set it; a working client must never, or the daemon
	// could idle out under it.
	probe bool
}

// AdmissionError reports a terminal negative admission outcome: a policy
// (fail, skip, cancel_others, or draining) rejected or evicted the run.
type AdmissionError struct {
	Policy       wingwire.Policy
	Key          string
	SupersededBy string
	// Reason is the daemon's one-line explanation naming the offending
	// input and its value for a malformed request. Empty for ordinary
	// policy rejections and older daemons, where only Policy and Key carry.
	Reason string
}

func (e *AdmissionError) Error() string {
	if e.Reason != "" {
		return "wingd: " + e.Reason
	}
	if e.SupersededBy != "" {
		return fmt.Sprintf("wingd: %s on %q, superseded by %s", e.Policy, e.Key, e.SupersededBy)
	}
	return fmt.Sprintf("wingd: %s on %q", e.Policy, e.Key)
}

// admissionError builds the terminal error for an eviction frame. When the
// daemon rejected the request as invalid but named no cause -- an older daemon
// that predates the reason field -- and this client is a different build than
// that daemon, it fills in a version-skew explanation so the opaque rejection
// is not the only thing the run sees.
func (cl *Client) admissionError(m *wingwire.Evicted) *AdmissionError {
	e := &AdmissionError{Policy: m.Policy, Key: m.Key, SupersededBy: m.SupersededBy, Reason: m.Reason}
	if e.Reason == "" && m.Key == "invalid" {
		if hint := cl.versionSkewHint(); hint != "" {
			e.Reason = hint
		}
	}
	return e
}

// versionSkewHint returns an explanation when this client and the daemon it is
// talking to are provably different builds, else "". Takeover replaces a
// daemon this client's build supersedes, but it cannot fire when either
// side's version is unknown or the daemon is the newer or dev-built side,
// and such a daemon rejects requests it cannot honor with a bare "invalid".
func (cl *Client) versionSkewHint() string {
	self, daemon := cl.opts.Version, cl.ack.BinaryVersion
	if self == "" || daemon == "" || self == daemon {
		return ""
	}
	return fmt.Sprintf("admission request rejected as invalid by daemon %s while this sparkwing is %s; a version skew can leave a running daemon unable to admit a newer client. Stop the daemon so the next run brings up a matching one, or run in an isolated SPARKWING_HOME", daemon, self)
}

// CancelledError reports that the daemon cancelled a run while it was
// still queued for admission -- the daemon pushed a [wingwire.Cancel]
// down the waiting connection instead of a grant. Reason is the short
// human phrase the daemon named. A caller maps it to a cancelled
// terminal status, the same category as an operator interrupt.
type CancelledError struct {
	Reason string
}

func (e *CancelledError) Error() string {
	if e.Reason == "" {
		return "wingd: run cancelled while queued"
	}
	return "wingd: " + e.Reason
}

// A detached spawn cannot distinguish slow initialization from process death.
// Keep one startup owner and allow its socket thirty seconds to appear.
// Starting replacements during that interval adds election contention and can
// prevent every otherwise healthy daemon from reaching readiness. The budget
// is wall-clock: how long the socket may take is a property of the machine,
// not of how often this client looks for it.
const (
	defaultBackoff   = 50 * time.Millisecond
	dialsPerSpawn    = 600
	maxSpawnAttempts = 1
)

// daemonStartupBudget is how long a spawned daemon has to bind its socket
// before this client reports it unreachable.
func daemonStartupBudget(opts Options) time.Duration {
	return time.Duration(dialsPerSpawn) * opts.backoff()
}

// maxTakeoverAttempts bounds how many times one connect drains the same
// daemon version and spawns its successor. A takeover that worked is
// followed by a connection to the new daemon, so needing several means
// the successor keeps coming up as the version it replaced -- a stuck
// binary, a stale spawn path -- and repeating it is a drain-respawn
// loop, not progress.
const maxTakeoverAttempts = 3

// maxTotalTakeovers bounds the drain-and-respawn exchanges one connect
// may run across every version it meets. Replacing a different
// predecessor each time is progress only while the population of
// predecessors shrinks; two old clients on a shared box, each respawning
// its own daemon, hand this one a version that is always new and never
// exhausts a per-version budget.
//
// The ceiling counts exchanges rather than wall-clock time because what
// this loop costs is not waiting: every attempt drains a live daemon and
// starts a process. A thirty-second deadline would permit hundreds of
// those; six bounds the side effect itself, while still covering a
// genuine handful of predecessors.
const maxTotalTakeovers = 2 * maxTakeoverAttempts

// takeoverBudget decides whether one connect may take another daemon
// over. It restarts the per-version allowance when the version changes,
// so a shrinking population of predecessors is not mistaken for a loop,
// and holds a total ceiling so an endless supply of new ones is.
type takeoverBudget struct {
	version    string
	perVersion int
	total      int
}

// spend records one takeover of the named daemon version, reporting
// false when the budget is gone and the skew has to be reported instead.
func (b *takeoverBudget) spend(version string) bool {
	if version != b.version {
		b.version, b.perVersion = version, 0
	}
	if b.perVersion >= maxTakeoverAttempts || b.total >= maxTotalTakeovers {
		return false
	}
	b.perVersion++
	b.total++
	return true
}

// ErrTakeoverExhausted reports that repeated takeovers did not produce a
// daemon this client can use. It is a version-skew fault an operator must
// resolve, not a wait that will clear.
var ErrTakeoverExhausted = errors.New("wingd/client: repeated daemon takeover did not resolve the version skew")

// errDaemonDraining is the cause a wait for a draining daemon reports if
// the caller's context ends first.
var errDaemonDraining = errors.New("wingd/client: daemon is draining")

// takeoverExhausted names both sides of the skew, because the useful fact
// is which two versions kept replacing each other.
func takeoverExhausted(selfVersion string, ack wingwire.HelloAck, attempts int) error {
	self := selfVersion
	if self == "" {
		self = "(unknown)"
	}
	daemon := ack.BinaryVersion
	if daemon == "" {
		daemon = "(unknown)"
	}
	return fmt.Errorf("%w: after %d attempts the daemon still reports %s (protocol %d) while this binary is %s (protocol %d). "+
		"Run `sparkwing daemon restart` to replace it, or set SPARKWING_HOME to run against a daemon of your own",
		ErrTakeoverExhausted, attempts, daemon, ack.ProtocolMajor, self, wingd.ProtocolMajor)
}

// spawnFailed reports why bringing a daemon up did not work. Most callers
// reach it with a spawn-syscall failure, and it folds in the daemon log
// tail when a prior attempt left one so a bind-time death is visible even
// when the final spawn is what erred. Some errors arrive already
// explained, and those pass through untouched.
//
// A dial that failed for a reason no spawn can fix -- the socket path blocked,
// a wedged listener -- outranks the spawn error, because that dial is the real
// obstacle and a spawn error reported over it sends the reader after the wrong
// process.
func spawnFailed(home, sock string, serr, dialErr error) error {
	if u := unreachable(sock, dialErr); u != nil {
		return u
	}
	if errors.Is(serr, ErrNoDaemon) || errors.Is(serr, ErrNoDaemonHost) {
		// Not a spawn failure: the caller declared it cannot or will not
		// start a daemon. The sentinel is the whole answer, and a leftover
		// log from some earlier daemon would only send the reader after a
		// process that is not the obstacle.
		return serr
	}
	if errors.Is(serr, ErrDaemonHostUnusable) || errors.Is(serr, ErrDaemonHostFailed) {
		// The obstacle is the named host binary, and the error already
		// names it, why it was chosen, and -- for a host that started and
		// died -- the tail of what it wrote. Re-wrapping it as a generic
		// spawn failure would bury the one fact the operator has to act
		// on, and appending the tail again would print it twice.
		return serr
	}
	if tail := daemonLogTail(home); tail != "" {
		path, _ := wingd.LogPath(home)
		return fmt.Errorf("wingd/client: spawn daemon: %w; daemon log %s:\n%s", serr, path, tail)
	}
	return fmt.Errorf("wingd/client: spawn daemon: %w", serr)
}

// daemonUnreachable reports that no daemon became reachable. It always wraps
// [ErrDaemonUnreachable], so every caller can tell this from an idle machine
// with one errors.Is rather than by reading the message.
//
// A detached spawn gives this client no reliable process-exit observation.
// A non-empty log therefore proves only that startup began, not that the
// daemon died. Report the failed readiness observation and retain the log as
// evidence without converting its last line into an exit diagnosis.
func daemonUnreachable(home, sock string, spawns int, cause, dialErr error) error {
	path, _ := wingd.LogPath(home)
	if spawns > 0 {
		if tail := daemonLogTail(home); tail != "" {
			attempt := "attempts"
			if spawns == 1 {
				attempt = "attempt"
			}
			return fmt.Errorf("%w: daemon did not become reachable after %d start %s: %w; daemon log %s:\n%s",
				ErrDaemonUnreachable, spawns, attempt, cause, path, tail)
		}
		return fmt.Errorf("%w: no daemon answered after %d start attempts; see %s: %w",
			ErrDaemonUnreachable, spawns, path, cause)
	}
	if u := unreachable(sock, dialErr); u != nil {
		return u
	}
	return fmt.Errorf("%w: %w", ErrDaemonUnreachable, cause)
}

// daemonDeathCause is the line of a dead daemon's log tail that names why it
// died: the last non-empty one. A daemon that cannot serve writes its reason
// and exits, so the reason is the last thing in the file, and the client has
// nothing better to go on because it never saw that daemon answer.
func daemonDeathCause(tail string) string {
	lines := strings.Split(strings.TrimRight(tail, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return strings.TrimPrefix(s, "sparkwing error: ")
		}
	}
	return "the daemon exited before serving"
}

// EnsureDaemon connects to Home's daemon, spawning one and retrying with
// backoff when none is reachable. When this client's build supersedes the
// daemon's -- a strictly newer release, an exact clean source build based on
// the daemon's release or later, or an ordered newer source build -- it drains
// the old daemon and brings up its own binary as the successor before returning
// a connection to it. [Options.NoTakeover] disables that replacement: the client
// shares the running daemon whatever its version, and fails with
// [ErrDaemonTooOld] only when the daemon's protocol major is below what this
// client speaks.
// The returned Client speaks the same protocol major and is ready for
// [Client.Acquire], [Client.Reattach], or [Client.QueueState]. When a
// spawned daemon dies at startup, the returned error carries the tail of
// its log and names the log path rather than reporting an unrelated
// spawn-layer failure.
func EnsureDaemon(ctx context.Context, opts Options) (*Client, error) {
	sock, err := wingd.SocketPath(opts.Home)
	if err != nil {
		return nil, err
	}
	if err := wingd.ValidateSocketPath(sock); err != nil {
		return nil, err
	}
	cl := &Client{opts: opts, sock: sock}
	if err := cl.connect(ctx); err != nil {
		return nil, err
	}
	return cl, nil
}

// connect dials the daemon into this client's connection, spawning one and
// retrying with backoff when none is reachable, and resolving a newer-client
// takeover. It is used both for the initial [EnsureDaemon] and to reconnect a
// client whose connection dropped on a daemon blink, so a reconnect reuses the
// exact spawn, handshake, and takeover path the first connect took.
//
// It carries the last dial failure out of the loop, because whether the socket
// refused with "nothing is listening" or "I could not reach the path" is the
// whole difference between an idle machine and a blind client, and the loop's
// final error is the only place left to say which.
func (cl *Client) connect(ctx context.Context) error {
	opts := cl.opts
	spawns := 0
	takeovers := &takeoverBudget{}
	drainWait := newRetry("wait for draining daemon", 0)
	dialWait := newRetryCapped("wait for daemon socket", 0, dialPaceMax)
	electionWait := newRetryCapped("wait for predecessor daemon", 0, electionPaceMax)
	var lastDial error
	var predecessorDeadline time.Time
	var socketDeadline time.Time
	for {
		if err := ctx.Err(); err != nil {
			return daemonUnreachable(opts.Home, cl.sock, spawns, err, lastDial)
		}
		nc, derr := dial(ctx, cl.sock, opts.dialTimeout())
		if derr != nil {
			lastDial = derr
			if socketDeadline.IsZero() || !time.Now().Before(socketDeadline) {
				if spawns >= maxSpawnAttempts {
					return daemonUnreachable(opts.Home, cl.sock, spawns, derr, lastDial)
				}
				preparation, lerr := wingd.PrepareDaemonSocket(opts.Home)
				if lerr != nil && preparation != wingd.SocketPreparationCleanupFailed {
					return spawnFailed(opts.Home, cl.sock, fmt.Errorf("check predecessor election: %w", lerr), lastDial)
				}
				switch preparation {
				case wingd.SocketPreparationElectionHeld:
					if predecessorDeadline.IsZero() {
						predecessorDeadline = time.Now().Add(opts.predecessorWaitTimeout())
						opts.logf("waiting for predecessor daemon election lock for %s", opts.Home)
					}
					if !time.Now().Before(predecessorDeadline) {
						cause := fmt.Errorf("predecessor daemon still holds the election lock for %s after %s", opts.Home, opts.predecessorWaitTimeout())
						return daemonUnreachable(opts.Home, cl.sock, spawns, cause, nil)
					}
					if err := electionWait.wait(ctx, derr); err != nil {
						return daemonUnreachable(opts.Home, cl.sock, spawns, err, lastDial)
					}
					continue
				case wingd.SocketPreparationCleanupFailed:
					if lerr == nil {
						return spawnFailed(opts.Home, cl.sock, errors.New("prepare daemon socket: cleanup failed without an error"), lastDial)
					}
					opts.logf("stale daemon socket cleanup failed before spawn: %v", lerr)
				case wingd.SocketPreparationReady:
					if lerr != nil {
						return spawnFailed(opts.Home, cl.sock, fmt.Errorf("prepare daemon socket: ready with error: %w", lerr), lastDial)
					}
				default:
					return spawnFailed(opts.Home, cl.sock, fmt.Errorf("prepare daemon socket: unexpected state %d", preparation), lastDial)
				}
				predecessorDeadline = time.Time{}
				electionWait.reset()
				if serr := opts.spawn(opts.Home, opts.Version); serr != nil {
					return spawnFailed(opts.Home, cl.sock, serr, lastDial)
				}
				spawns++
				socketDeadline = time.Now().Add(daemonStartupBudget(opts))
				dialWait.reset()
			}
			if err := dialWait.wait(ctx, derr); err != nil {
				return daemonUnreachable(opts.Home, cl.sock, spawns, err, lastDial)
			}
			continue
		}
		cl.nc = nc
		cl.dec = newFrameReader(nc)
		ack, herr := cl.handshake(opts.Version)
		if herr != nil {
			cl.Close()
			if err := sleep(ctx, opts.backoff()); err != nil {
				return daemonUnreachable(opts.Home, cl.sock, spawns, err, lastDial)
			}
			continue
		}

		if ack.ProtocolMajor != wingd.ProtocolMajor {
			if wingd.ProtocolMajor > ack.ProtocolMajor {
				if opts.NoTakeover {
					// safety: reported before any budget is spent, so a
					// client that never takes over can never exhaust the
					// takeover allowance and report the wrong fault.
					cl.Close()
					return daemonTooOld(opts.Version, ack)
				}
				cl.ack = ack
				if !takeovers.spend(ack.BinaryVersion) {
					cl.Close()
					return takeoverExhausted(opts.Version, ack, takeovers.total)
				}
				if terr := cl.takeover(ctx, opts); terr != nil {
					return terr
				}
				continue
			}
			cl.Close()
			return protocolTooOld(opts.Version, ack)
		}
		if !opts.NoTakeover && !servedDownLevel(ack) && supersedes(opts.Version, ack.BinaryVersion) {
			cl.ack = ack
			if !takeovers.spend(ack.BinaryVersion) {
				_ = cl.Close()
				return takeoverExhausted(opts.Version, ack, takeovers.total)
			}
			if terr := cl.takeover(ctx, opts); terr != nil {
				return terr
			}
			continue
		}
		if !servedDownLevel(ack) {
			if err := buildMismatch(opts.Version, ack); err != nil {
				cl.Close()
				return err
			}
		}
		if ack.Draining {
			cl.Close()
			// safety: a drain finishes only when the last holder leaves, which can take as long as a run does, so this waits without a cap -- but with backoff, since re-dialing a draining daemon at full speed is the spin this loop must not become.
			if err := drainWait.wait(ctx, errDaemonDraining); err != nil {
				return err
			}
			continue
		}
		cl.ack = ack
		// safety: a live connection clears any closed mark an intermediate failed attempt set, so later frame-read recovery still runs.
		cl.closed.Store(false)
		return nil
	}
}

// defaultReattachTimeout bounds a mid-operation reconnect so a frame-read
// recovery cannot hang forever when the daemon does not come back. It is
// generous enough to cover a daemon respawn and the reattach handshake within
// a typical grace window.
const defaultReattachTimeout = 8 * time.Second

// reconnect re-establishes this client's connection to the daemon after a
// blink, bounding the attempt so a daemon that never returns fails loud rather
// than hanging. On failure it names the daemon lifecycle event and folds in the
// daemon log tail, so a run sees "the daemon restarted and did not come back"
// with the cause one file away rather than a bare closed-connection error.
func (cl *Client) reconnect(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(ctx, defaultReattachTimeout)
	defer cancel()
	if err := cl.connect(rctx); err != nil {
		return fmt.Errorf("wingd/client: admission daemon restarted and did not come back: %w", err)
	}
	return nil
}

// recoverConn decides what to do when a frame-read failed. When ctx is done
// the caller abandoned the operation, so it returns the context error without
// reconnecting; otherwise the failure is a daemon blink and it reconnects so
// the operation can be re-driven on the fresh connection.
func (cl *Client) recoverConn(ctx context.Context) error {
	if cl.closed.Load() {
		return net.ErrClosed
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return cl.reconnect(ctx)
}

// takeover drains the reachable older daemon and spawns this client's
// binary as its successor, then returns so the caller re-dials.
//
// It returns the successor spawn's failure when that failure is the host
// binary itself dying, so a takeover into a broken host fails in
// milliseconds naming the binary rather than draining a working daemon
// and then spending the full socket budget waiting for a replacement that
// cannot come. Every other spawn error stays a logged best-effort: the
// old daemon has already been drained, so re-dialing is still the right
// next move and the connect loop's own budget covers it.
func (cl *Client) takeover(ctx context.Context, opts Options) error {
	opts.logf("taking over daemon %s with %s", cl.ack.BinaryVersion, opts.Version)
	_ = cl.nc.SetWriteDeadline(time.Now().Add(opts.dialTimeout()))
	_ = cl.write(&wingwire.DrainRequest{SuccessorVersion: opts.Version})
	_ = cl.nc.SetReadDeadline(time.Now().Add(opts.dialTimeout()))
	_, _ = cl.dec.read()
	cl.Close()
	if err := opts.spawn(opts.Home, opts.Version); err != nil {
		if errors.Is(err, ErrDaemonHostFailed) || errors.Is(err, ErrDaemonHostUnusable) {
			return err
		}
		opts.logf("spawn successor: %v", err)
	}
	_ = sleep(ctx, opts.backoff())
	return nil
}

func (cl *Client) handshake(version string) (wingwire.HelloAck, error) {
	if err := cl.write(&wingwire.Hello{ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: version, BuildIdentity: wingwire.BuildIdentity, HealthProbe: cl.probe, HolderLiveness: !cl.probe}); err != nil {
		return wingwire.HelloAck{}, err
	}
	msg, err := cl.dec.read()
	if err != nil {
		return wingwire.HelloAck{}, err
	}
	ack, ok := msg.(*wingwire.HelloAck)
	if !ok {
		return wingwire.HelloAck{}, fmt.Errorf("wingd/client: expected hello_ack, got %T", msg)
	}
	return *ack, nil
}

// Draining reports whether the connected daemon said it is draining. A
// caller that needs a durable lease should retry [EnsureDaemon].
func (cl *Client) Draining() bool { return cl.ack.Draining }

// DaemonVersion is the connected daemon's reported binary version.
func (cl *Client) DaemonVersion() string { return cl.ack.BinaryVersion }

func (cl *Client) write(msg wingwire.Message) error {
	line, err := wingwire.Encode(msg)
	if err != nil {
		return err
	}
	_, err = cl.nc.Write(line)
	return err
}

// Close ends the connection. For a held lease this releases it -- the
// daemon reacts to the socket closing.
func (cl *Client) Close() error {
	if cl.nc == nil {
		return nil
	}
	// safety: mark closed before closing the socket so a Watch or Acquire
	// reader that wakes on the close sees the intent and exits instead of
	// reconnecting to a daemon the caller is done with.
	cl.closed.Store(true)
	return cl.nc.Close()
}
