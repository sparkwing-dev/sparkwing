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

var ErrProtocolTooOld = errors.New("wingd/client: daemon no longer serves this client's protocol")

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

var ErrDaemonTooOld = errors.New("wingd/client: daemon protocol is older than this client")

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

const FirstHostingRelease = "v0.27.0"

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

func servedDownLevel(ack wingwire.HelloAck) bool {
	return ack.NativeProtocolMajor > ack.ProtocolMajor
}

var ErrReattachRejected = errors.New("wingd/client: re-attach rejected; lease is gone")

type Options struct {
	Home string

	Version string

	Spawn func(home, version string) error

	NoTakeover bool

	DialTimeout time.Duration

	Backoff time.Duration

	PredecessorWaitTimeout time.Duration

	Logf               func(format string, args ...any)
	healthProbe        bool
	observeDialFailure func()
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

type Client struct {
	nc   net.Conn
	dec  *frameReader
	ack  wingwire.HelloAck
	opts Options
	sock string

	closed atomic.Bool

	probe bool
}

type AdmissionError struct {
	Policy       wingwire.Policy
	Key          string
	SupersededBy string

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

func (cl *Client) admissionError(m *wingwire.Evicted) *AdmissionError {
	e := &AdmissionError{Policy: m.Policy, Key: m.Key, SupersededBy: m.SupersededBy, Reason: m.Reason}
	if e.Reason == "" && m.Key == "invalid" {
		if hint := cl.versionSkewHint(); hint != "" {
			e.Reason = hint
		}
	}
	return e
}

func (cl *Client) versionSkewHint() string {
	self, daemon := cl.opts.Version, cl.ack.BinaryVersion
	if self == "" || daemon == "" || self == daemon {
		return ""
	}
	return fmt.Sprintf("admission request rejected as invalid by daemon %s while this sparkwing is %s; a version skew can leave a running daemon unable to admit a newer client. Stop the daemon so the next run brings up a matching one, or run in an isolated SPARKWING_HOME", daemon, self)
}

type CancelledError struct {
	Reason string
}

func (e *CancelledError) Error() string {
	if e.Reason == "" {
		return "wingd: run cancelled while queued"
	}
	return "wingd: " + e.Reason
}

const (
	defaultBackoff   = 50 * time.Millisecond
	dialsPerSpawn    = 600
	maxSpawnAttempts = 1
)

func daemonStartupBudget(opts Options) time.Duration {
	return time.Duration(dialsPerSpawn) * opts.backoff()
}

const maxTakeoverAttempts = 3

const maxTotalTakeovers = 2 * maxTakeoverAttempts

type takeoverBudget struct {
	version    string
	perVersion int
	total      int
}

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

var ErrTakeoverExhausted = errors.New("wingd/client: repeated daemon takeover did not resolve the version skew")

var errDaemonDraining = errors.New("wingd/client: daemon is draining")

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

func spawnFailed(home, sock string, serr, dialErr error) error {
	if u := unreachable(sock, dialErr); u != nil {
		return u
	}
	if errors.Is(serr, ErrNoDaemon) || errors.Is(serr, ErrNoDaemonHost) {
		return serr
	}
	if errors.Is(serr, ErrDaemonHostUnusable) || errors.Is(serr, ErrDaemonHostFailed) {
		return serr
	}
	if tail := daemonLogTail(home); tail != "" {
		path, _ := wingd.LogPath(home)
		return fmt.Errorf("wingd/client: spawn daemon: %w; daemon log %s:\n%s", serr, path, tail)
	}
	return fmt.Errorf("wingd/client: spawn daemon: %w", serr)
}

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

func describeHome(home string) string {
	if resolved, err := wingd.HomeDir(home); err == nil && resolved != "" {
		return resolved
	}
	return home
}

func daemonDeathCause(tail string) string {
	lines := strings.Split(strings.TrimRight(tail, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return strings.TrimPrefix(s, "sparkwing error: ")
		}
	}
	return "the daemon exited before serving"
}

func EnsureDaemon(ctx context.Context, opts Options) (*Client, error) {
	sock, err := wingd.SocketPath(opts.Home)
	if err != nil {
		return nil, err
	}
	if err := wingd.ValidateSocketPath(sock); err != nil {
		return nil, err
	}
	cl := &Client{opts: opts, sock: sock, probe: opts.healthProbe}
	if err := cl.connect(ctx); err != nil {
		return nil, err
	}
	return cl, nil
}

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
			if opts.observeDialFailure != nil {
				opts.observeDialFailure()
			}
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
						opts.logf("waiting for predecessor daemon election lock for %s", describeHome(opts.Home))
					}
					if !time.Now().Before(predecessorDeadline) {
						cause := fmt.Errorf("predecessor daemon still holds the election lock for %s after %s", describeHome(opts.Home), opts.predecessorWaitTimeout())
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
			// safety: draining can last for a full run; wait without a time cap but
			// retain backoff to avoid a reconnect spin.
			if err := drainWait.wait(ctx, errDaemonDraining); err != nil {
				return err
			}
			continue
		}
		cl.ack = ack
		// safety: clear intermediate closed marks after reconnect so later frame
		// failures can still trigger recovery.
		cl.closed.Store(false)
		return nil
	}
}

const defaultReattachTimeout = 8 * time.Second

func (cl *Client) reconnect(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(ctx, defaultReattachTimeout)
	defer cancel()
	if err := cl.connect(rctx); err != nil {
		return fmt.Errorf("wingd/client: admission daemon restarted and did not come back: %w", err)
	}
	return nil
}

func (cl *Client) recoverConn(ctx context.Context) error {
	if cl.closed.Load() {
		return net.ErrClosed
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return cl.reconnect(ctx)
}

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

func (cl *Client) Draining() bool { return cl.ack.Draining }

func (cl *Client) DaemonVersion() string { return cl.ack.BinaryVersion }

func (cl *Client) write(msg wingwire.Message) error {
	line, err := wingwire.Encode(msg)
	if err != nil {
		return err
	}
	_, err = cl.nc.Write(line)
	return err
}

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
