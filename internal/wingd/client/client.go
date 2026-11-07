package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

var errProtocolTooOld = errors.New("wingd/client: daemon no longer serves this client's protocol")

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
	return fmt.Errorf(
		"%w: daemon speaks protocol %d (sparkwing %s), this pipeline binary speaks protocol %d (sparkwing %s). "+
			"%s and re-run, or set SPARKWING_HOME to run against a daemon of your own; "+
			"run `sparkwing pipeline upgrade` to update this pipeline pin",
		errProtocolTooOld, ack.ProtocolMajor, daemon, wingd.ProtocolMajor, self, pinAdvice)
}

var errDaemonTooOld = errors.New("wingd/client: daemon protocol is older than this client")

var errBuildMismatch = errors.New("wingd/client: daemon build differs from this client")

// IncompatibleDaemon reports a running daemon whose protocol or build this client cannot use.
func IncompatibleDaemon(err error) bool {
	return errors.Is(err, errDaemonTooOld) || errors.Is(err, errProtocolTooOld) || errors.Is(err, errBuildMismatch)
}

func buildMismatch(selfVersion string, ack wingwire.HelloAck) error {
	self := strings.TrimSpace(selfVersion)
	daemon := strings.TrimSpace(ack.BinaryVersion)
	// safety: the HTTP payload contract requires the same admission build identity.
	if ack.BuildIdentity == wingwire.BuildIdentity {
		return nil
	}
	if self == "" {
		self = "(unknown)"
	}
	if daemon == "" {
		daemon = "(unknown)"
	}
	return fmt.Errorf("%w: this build is %s and the daemon is %s; the same protocol major does not prove same-build compatibility. When no runs are active, run `sparkwing daemon restart --force` with this build or use a separate SPARKWING_HOME",
		errBuildMismatch, self, daemon)
}

const firstHostingRelease = "v0.27.0"

func minHostingRelease() string {
	floor, known := wingwire.ReleasedProtocolFloors().MinVersionSpeaking(wingd.ProtocolMajor)
	if !known {
		return firstHostingRelease
	}
	if semver.Compare(floor, firstHostingRelease) > 0 {
		return floor
	}
	return firstHostingRelease
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
	return fmt.Errorf(
		"%w: daemon speaks protocol %d (sparkwing %s), this pipeline binary speaks protocol %d (sparkwing %s). "+
			"Install sparkwing %s or newer on this host with `sparkwing update`, then run `sparkwing daemon restart --force` when no runs are active",
		errDaemonTooOld, ack.ProtocolMajor, daemon, wingd.ProtocolMajor, self, minHostingRelease())
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
	// safety: a non-hosting client cannot start wingd by re-executing itself.
	if o.NoTakeover {
		return errNoDaemonHost
	}
	return defaultSpawn(home, version)
}

type Client struct {
	ack        wingwire.HelloAck
	opts       Options
	sock       string
	closed     atomic.Bool
	lifetimeMu sync.Mutex
	lifetime   context.Context
	cancel     context.CancelFunc
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

type cancelledError struct {
	Reason string
}

func (e *cancelledError) Error() string {
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

var errDaemonDraining = errors.New("wingd/client: daemon is draining")

func spawnFailed(home, sock string, serr, dialErr error) error {
	if u := unreachable(sock, dialErr); u != nil {
		return u
	}
	if errors.Is(serr, ErrNoDaemon) || errors.Is(serr, errNoDaemonHost) {
		return serr
	}
	if errors.Is(serr, errDaemonHostFailed) {
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
				errDaemonUnreachable, spawns, attempt, cause, path, tail)
		}
		return fmt.Errorf("%w: no daemon answered after %d start attempts; see %s: %w",
			errDaemonUnreachable, spawns, path, cause)
	}
	if u := unreachable(sock, dialErr); u != nil {
		return errors.Join(u, cause)
	}
	return fmt.Errorf("%w: %w", errDaemonUnreachable, cause)
}

func describeHome(home string) string {
	if resolved, err := wingd.HomeDir(home); err == nil && resolved != "" {
		return resolved
	}
	return home
}

func EnsureDaemon(ctx context.Context, opts Options) (*Client, error) {
	sock, err := wingd.EnsureSocketPath(opts.Home)
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

func (cl *Client) connect(ctx context.Context) error {
	return cl.connectPaced(ctx, dialPaceMax)
}

func (cl *Client) connectPaced(ctx context.Context, dialMax time.Duration) error {
	opts := cl.opts
	spawns := 0
	drainWait := newRetry("wait for draining daemon", 0)
	dialWait := newRetryCapped("wait for daemon socket", 0, dialMax)
	electionWait := newRetryCapped("wait for predecessor daemon", 0, max(dialMax, electionPaceMax))
	var lastDial error
	var predecessorDeadline time.Time
	var socketDeadline time.Time
	releaseStart := func() {}
	defer func() { releaseStart() }()
	for {
		if err := ctx.Err(); err != nil {
			return daemonUnreachable(opts.Home, cl.sock, spawns, err, lastDial)
		}
		nc, derr := dial(ctx, wingd.APISocketBeside(cl.sock), opts.dialTimeout())
		if derr != nil {
			if opts.observeDialFailure != nil {
				opts.observeDialFailure()
			}
			lastDial = derr
			if err := ctx.Err(); err != nil {
				return daemonUnreachable(opts.Home, cl.sock, spawns, err, lastDial)
			}
			if socketDeadline.IsZero() || !time.Now().Before(socketDeadline) {
				releaseStart()
				releaseStart = func() {}
				if spawns >= maxSpawnAttempts {
					return daemonUnreachable(opts.Home, cl.sock, spawns, derr, lastDial)
				}
				release, claimed, cerr := wingd.ClaimDaemonStart(opts.Home)
				if cerr != nil {
					return spawnFailed(opts.Home, cl.sock, fmt.Errorf("claim daemon start: %w", cerr), lastDial)
				}
				if !claimed {
					if err := dialWait.wait(ctx, derr); err != nil {
						return daemonUnreachable(opts.Home, cl.sock, spawns, err, lastDial)
					}
					continue
				}
				releaseStart = release
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
				if err := ctx.Err(); err != nil {
					return daemonUnreachable(opts.Home, cl.sock, spawns, err, lastDial)
				}
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
		releaseStart()
		releaseStart = func() {}
		if err := nc.Close(); err != nil {
			return err
		}
		var ack wingwire.HelloAck
		pctx, cancel := context.WithTimeout(ctx, connectHandshakeTimeout)
		herr := cl.apiCall(pctx, http.MethodGet, controllerPath+"probe", nil, &ack)
		cancel()
		if herr != nil {
			if !transientAPIError(herr) {
				return fmt.Errorf("%w: this sparkwing is %s; the controller does not serve the HTTP admission contract. Run `sparkwing daemon restart --force` with this build when no runs are active: %w", errDaemonTooOld, opts.Version, herr)
			}
			if err := sleep(ctx, opts.backoff()); err != nil {
				return daemonUnreachable(opts.Home, cl.sock, spawns, err, lastDial)
			}
			continue
		}

		if ack.ProtocolMajor != wingd.ProtocolMajor {
			if wingd.ProtocolMajor > ack.ProtocolMajor {
				return errors.Join(daemonTooOld(opts.Version, ack), cl.Close())
			}
			return errors.Join(protocolTooOld(opts.Version, ack), cl.Close())
		}
		if err := buildMismatch(opts.Version, ack); err != nil {
			return err
		}
		if ack.Draining {
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

const connectHandshakeTimeout = time.Minute

func (cl *Client) DaemonVersion() string { return cl.ack.BinaryVersion }

func (cl *Client) clientContext(ctx context.Context) context.Context {
	cl.lifetimeMu.Lock()
	defer cl.lifetimeMu.Unlock()
	if cl.lifetime == nil {
		cl.lifetime, cl.cancel = context.WithCancel(context.WithoutCancel(ctx))
		if cl.closed.Load() {
			cl.cancel()
		}
	}
	return cl.lifetime
}

func (cl *Client) Close() error {
	cl.lifetimeMu.Lock()
	defer cl.lifetimeMu.Unlock()
	cl.closed.Store(true)
	if cl.cancel != nil {
		cl.cancel()
	}
	return nil
}
