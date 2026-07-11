// Package wingd implements sparkwingd, the single local admission
// arbiter. One daemon per sparkwing home owns the admission ledger
// (internal/admission), serves clients over a unix socket speaking the
// wingwire protocol (pkg/wingwire), and derives liveness from the socket
// itself: a lease is held by an open connection, so a client's death is
// reported by the kernel closing the socket, with no heartbeats or
// polling anywhere.
//
// # Election and lifecycle
//
// The daemon elects itself with an exclusive flock on a lock file under
// the sparkwing home; the loser of a race exits with [ErrNotElected] and
// its clients connect to the winner. The socket lives at a stable path in
// the same directory. A daemon with nothing to do -- no leases, no
// waiters, no connections for an idle window -- snapshots and exits.
//
// # Durable state and takeover
//
// Every transition writes the ledger snapshot through to a state file by
// atomic rename. On start with existing state the daemon restores the
// ledger and holds a grace window during which clients reclaim their
// leases by presenting the re-attach token from their [wingwire.Grant];
// leases nobody reclaims are released at the window's end. Crash recovery
// and version takeover share this one path: a newer client drains the old
// daemon, which snapshots and exits, and the successor restores the same
// state and honors the same grace window.
//
// # Host sensing
//
// A [HostSampler] feeds measured load and free memory into
// [admission.Ledger.SetHeadroom] with hysteresis, reserving a
// configurable margin so heavy work is admitted only into real headroom.
package wingd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// ErrNotElected is returned by [Daemon.Run] when another daemon already
// holds the election lock for this sparkwing home. It is not a failure:
// the caller should exit successfully and let its clients connect to the
// elected daemon.
var ErrNotElected = errors.New("wingd: another daemon is already elected")

// Default lifecycle windows. All are overridable via [Config] so tests
// can compress them.
const (
	// DefaultIdleTimeout is how long a daemon with no leases, waiters, or
	// connections waits before exiting.
	DefaultIdleTimeout = 5 * time.Minute
	// DefaultGraceWindow is how long a freshly started daemon holds
	// restored leases open for re-attach before releasing the unclaimed
	// ones.
	DefaultGraceWindow = 10 * time.Second
	// DefaultSampleInterval is the period between host-load samples.
	DefaultSampleInterval = 5 * time.Second
	// DefaultHeadroomFraction is the share of host capacity reserved and
	// never offered to admission.
	DefaultHeadroomFraction = 0.20
)

// Config parameterizes a [Daemon]. Only Home is required; every other
// field has a working default.
type Config struct {
	// Home is the sparkwing home directory. The daemon places its lock,
	// socket, and state file in a wingd subdirectory of it.
	Home string
	// Version is this binary's version, reported in [wingwire.HelloAck]
	// and compared against connecting clients to decide takeover. Empty is
	// treated as an unknown version that never triggers takeover.
	Version string
	// HeadroomFraction is the reserved share of host capacity (0..1). Zero
	// uses [DefaultHeadroomFraction]; a negative value disables the
	// reserve.
	HeadroomFraction float64
	// Sampler reads host capacity and pressure. Nil uses the real
	// platform sampler.
	Sampler HostSampler
	// Now returns the current time; nil uses time.Now. Injected so tests
	// can measure elapsed hold time deterministically.
	Now func() time.Time
	// IdleTimeout overrides [DefaultIdleTimeout] when non-zero.
	IdleTimeout time.Duration
	// GraceWindow overrides [DefaultGraceWindow] when non-zero. A negative
	// value collapses the window to zero (release unclaimed leases at
	// once).
	GraceWindow time.Duration
	// SampleInterval overrides [DefaultSampleInterval] when non-zero.
	SampleInterval time.Duration
	// FinalizeRun, when set, is called with a run ID whose client
	// disconnected while still holding or awaiting admission -- the
	// process died without releasing (SIGKILL, panic). The callee
	// finalizes the orphaned run row so it does not sit in a running
	// state forever; it must tolerate rows that are already terminal or
	// absent. Called on its own goroutine, never under daemon locks.
	FinalizeRun func(runID string)
	// Logf, when set, receives one-line operational messages. Nil
	// discards them.
	Logf func(format string, args ...any)
}

func (c Config) idleTimeout() time.Duration {
	if c.IdleTimeout > 0 {
		return c.IdleTimeout
	}
	return DefaultIdleTimeout
}

func (c Config) graceWindow() time.Duration {
	if c.GraceWindow < 0 {
		return 0
	}
	if c.GraceWindow > 0 {
		return c.GraceWindow
	}
	return DefaultGraceWindow
}

func (c Config) sampleInterval() time.Duration {
	if c.SampleInterval > 0 {
		return c.SampleInterval
	}
	return DefaultSampleInterval
}

func (c Config) headroomFraction() float64 {
	if c.HeadroomFraction < 0 {
		return 0
	}
	if c.HeadroomFraction == 0 {
		return DefaultHeadroomFraction
	}
	return c.HeadroomFraction
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Config) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// layout resolves the on-disk paths a daemon uses under a sparkwing home.
type layout struct {
	dir   string
	lock  string
	sock  string
	state string
}

func resolveLayout(home string) (layout, error) {
	if home == "" {
		p, err := paths.DefaultPaths()
		if err != nil {
			return layout{}, fmt.Errorf("wingd: resolve home: %w", err)
		}
		home = p.Root
	}
	dir := filepath.Join(home, "wingd")
	return layout{
		dir:   dir,
		lock:  filepath.Join(dir, "d.lock"),
		sock:  filepath.Join(dir, "d.sock"),
		state: filepath.Join(dir, "state.json"),
	}, nil
}

func (l layout) ensureDir() error {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return fmt.Errorf("wingd: prepare %s: %w", l.dir, err)
	}
	return nil
}

// SocketPath returns the unix socket path a daemon serving home binds,
// which clients connect to. Exposed so the client library and tests agree
// on the address without duplicating the layout rule.
func SocketPath(home string) (string, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return "", err
	}
	return l.sock, nil
}

// LockPath returns the election lock file path for home.
func LockPath(home string) (string, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return "", err
	}
	return l.lock, nil
}

// ProtocolMajor is the wire protocol major this daemon speaks; it mirrors
// [wingwire.ProtocolMajor].
const ProtocolMajor = wingwire.ProtocolMajor
