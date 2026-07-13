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
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// ErrProtocolTooOld is returned when the running daemon speaks a newer
// protocol major than this client, which cannot be resolved by takeover:
// the client binary must be upgraded.
var ErrProtocolTooOld = errors.New("wingd/client: daemon speaks a newer protocol; upgrade sparkwing")

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
	// decide whether to take over an older daemon. Empty never triggers
	// takeover.
	Version string
	// Spawn starts a detached daemon for Home. Nil uses the default, which
	// re-execs this binary as `sparkwing wingd run`.
	Spawn func(home, version string) error
	// DialTimeout bounds a single connect attempt. Zero uses a small
	// default.
	DialTimeout time.Duration
	// Backoff is the base wait between spawn-and-retry attempts. Zero uses
	// a small default.
	Backoff time.Duration
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
	return 50 * time.Millisecond
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
}

// AdmissionError reports a terminal negative admission outcome: a policy
// (fail, skip, cancel_others, or draining) rejected or evicted the run.
type AdmissionError struct {
	Policy       wingwire.Policy
	Key          string
	SupersededBy string
}

func (e *AdmissionError) Error() string {
	if e.SupersededBy != "" {
		return fmt.Sprintf("wingd: %s on %q, superseded by %s", e.Policy, e.Key, e.SupersededBy)
	}
	return fmt.Sprintf("wingd: %s on %q", e.Policy, e.Key)
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

// dialsPerSpawn is how many times connect is retried after a spawn before
// the daemon is presumed dead and respawned; maxSpawnAttempts bounds the
// respawns so a daemon that dies at startup fails fast with its own logged
// cause rather than spinning until a fork exhaustion error masks it.
const (
	dialsPerSpawn    = 5
	maxSpawnAttempts = 4
)

// spawnFailed reports a spawn-syscall failure, folding in the daemon log
// tail when a prior attempt left one so a bind-time death is visible even
// when the final spawn is what erred.
func spawnFailed(home string, serr error) error {
	if tail := daemonLogTail(home); tail != "" {
		path, _ := wingd.LogPath(home)
		return fmt.Errorf("wingd/client: spawn daemon: %w; daemon log %s:\n%s", serr, path, tail)
	}
	return fmt.Errorf("wingd/client: spawn daemon: %w", serr)
}

// daemonUnreachable reports that no daemon became reachable. When one was
// spawned it distinguishes a daemon that started then exited before
// serving (surfacing the tail of its log) from a plain timeout, and always
// names the log path so the real cause is one file away.
func daemonUnreachable(home string, spawns int, cause error) error {
	path, _ := wingd.LogPath(home)
	if spawns > 0 {
		if tail := daemonLogTail(home); tail != "" {
			return fmt.Errorf("wingd/client: admission daemon started but exited before serving; daemon log %s:\n%s", path, tail)
		}
		return fmt.Errorf("wingd/client: admission daemon did not become reachable after %d start attempts; see %s: %w", spawns, path, cause)
	}
	return fmt.Errorf("wingd/client: could not reach admission daemon: %w", cause)
}

// EnsureDaemon connects to Home's daemon, spawning one and retrying with
// backoff when none is reachable. When this client's version is ahead of
// the daemon's it drains the old daemon and brings up its own binary as
// the successor before returning a connection to it. The returned Client
// speaks the same protocol major and is ready for [Client.Acquire],
// [Client.Reattach], or [Client.QueueState]. When a spawned daemon dies at
// startup, the returned error carries the tail of its log and names the
// log path rather than reporting an unrelated spawn-layer failure.
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
func (cl *Client) connect(ctx context.Context) error {
	opts := cl.opts
	spawns := 0
	dialsSinceSpawn := 0
	for {
		if err := ctx.Err(); err != nil {
			return daemonUnreachable(opts.Home, spawns, err)
		}
		nc, derr := dial(ctx, cl.sock, opts.dialTimeout())
		if derr != nil {
			if spawns == 0 || dialsSinceSpawn >= dialsPerSpawn {
				if spawns >= maxSpawnAttempts {
					return daemonUnreachable(opts.Home, spawns, derr)
				}
				_, _ = wingd.RemoveStaleSocket(opts.Home)
				if serr := opts.spawn(opts.Home, opts.Version); serr != nil {
					return spawnFailed(opts.Home, serr)
				}
				spawns++
				dialsSinceSpawn = 0
			} else {
				dialsSinceSpawn++
			}
			if err := sleep(ctx, opts.backoff()); err != nil {
				return daemonUnreachable(opts.Home, spawns, err)
			}
			continue
		}
		cl.nc = nc
		cl.dec = newFrameReader(nc)
		ack, herr := cl.handshake(opts.Version)
		if herr != nil {
			cl.Close()
			if err := sleep(ctx, opts.backoff()); err != nil {
				return daemonUnreachable(opts.Home, spawns, err)
			}
			continue
		}

		if ack.ProtocolMajor != wingd.ProtocolMajor {
			if wingd.ProtocolMajor > ack.ProtocolMajor {
				cl.ack = ack
				cl.takeover(ctx, opts)
				continue
			}
			cl.Close()
			return ErrProtocolTooOld
		}
		if versionNewer(opts.Version, ack.BinaryVersion) {
			cl.ack = ack
			cl.takeover(ctx, opts)
			continue
		}
		if ack.Draining {
			cl.Close()
			if err := sleep(ctx, opts.backoff()); err != nil {
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
func (cl *Client) takeover(ctx context.Context, opts Options) {
	opts.logf("taking over daemon %s with %s", cl.ack.BinaryVersion, opts.Version)
	_ = cl.nc.SetWriteDeadline(time.Now().Add(opts.dialTimeout()))
	_ = cl.write(&wingwire.DrainRequest{SuccessorVersion: opts.Version})
	_ = cl.nc.SetReadDeadline(time.Now().Add(opts.dialTimeout()))
	_, _ = cl.dec.read()
	cl.Close()
	if err := opts.spawn(opts.Home, opts.Version); err != nil {
		opts.logf("spawn successor: %v", err)
	}
	_ = sleep(ctx, opts.backoff())
}

func (cl *Client) handshake(version string) (wingwire.HelloAck, error) {
	if err := cl.write(&wingwire.Hello{ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: version}); err != nil {
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
