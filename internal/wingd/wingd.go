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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	// DefaultCapacityInterval is how often the daemon re-derives machine
	// capacity while running, so an instance resize or a cgroup-quota edit is
	// picked up without a restart. Slower than the load sampler because fixed
	// capacity moves rarely.
	DefaultCapacityInterval = 60 * time.Second
	// DefaultHeadroomFraction is the share of host capacity reserved and
	// never offered to admission.
	DefaultHeadroomFraction = 0.20
	// DefaultStallInterval is how often a holder's process CPU is
	// sampled while runs are queued behind it.
	DefaultStallInterval = 10 * time.Second
	// DefaultStallWindow is how long a holder must stay below the CPU
	// threshold, with waiters present, before it is flagged stalled.
	DefaultStallWindow = 60 * time.Second
	// DefaultStallCPUFraction is the per-core CPU fraction below which a
	// holder counts as idle for stall detection.
	DefaultStallCPUFraction = 0.02
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
	// Budget caps the admission ledger below the machine total and,
	// when it opts in, hardens the cap at the OS level. A zero Budget
	// leaves the full machine available, the historical behavior.
	Budget Budget
	// Sampler reads host capacity and pressure. Nil uses the real
	// platform sampler.
	Sampler HostSampler
	// ContainerRoot is the filesystem root under which the daemon reads its
	// own cgroup limits to clamp capacity to the container it runs in. Empty
	// reads the real filesystem at "/" for the real platform sampler and
	// disables detection when a Sampler is injected; a test points it at a
	// fixture cgroup tree to exercise container-aware capacity.
	ContainerRoot string
	// ProcSampler reads a holder process's CPU for stall flagging. Nil
	// uses the real platform sampler; a sampler that reports not-sampled
	// (unsupported platforms) simply leaves holders unflagged.
	ProcSampler ProcSampler
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
	// CapacityInterval overrides [DefaultCapacityInterval] when non-zero,
	// setting how often capacity is re-derived while the daemon runs.
	CapacityInterval time.Duration
	// StallInterval overrides [DefaultStallInterval] when non-zero.
	StallInterval time.Duration
	// StallWindow overrides [DefaultStallWindow] when non-zero.
	StallWindow time.Duration
	// StallCPUFraction overrides [DefaultStallCPUFraction] when non-zero;
	// a negative value disables stall flagging entirely.
	StallCPUFraction float64
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

func (c Config) capacityInterval() time.Duration {
	if c.CapacityInterval > 0 {
		return c.CapacityInterval
	}
	return DefaultCapacityInterval
}

func (c Config) stallInterval() time.Duration {
	if c.StallInterval > 0 {
		return c.StallInterval
	}
	return DefaultStallInterval
}

func (c Config) stallWindow() time.Duration {
	if c.StallWindow > 0 {
		return c.StallWindow
	}
	return DefaultStallWindow
}

// stallCPUFraction is the idle threshold; a negative config value returns
// zero, which disables flagging because no reading is ever below it.
func (c Config) stallCPUFraction() float64 {
	if c.StallCPUFraction < 0 {
		return 0
	}
	if c.StallCPUFraction == 0 {
		return DefaultStallCPUFraction
	}
	return c.StallCPUFraction
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

// layout resolves the on-disk paths a daemon uses for a sparkwing home.
// The lock, state, log, and socket-pointer record live under the home
// directory, which has no length limit; only the socket itself is placed
// on a short hashed path so a deep home cannot push it past the OS
// sun_path limit and break bind.
type layout struct {
	dir    string
	lock   string
	sock   string
	state  string
	log    string
	record string
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
		dir:    dir,
		lock:   filepath.Join(dir, "d.lock"),
		sock:   socketPathForHome(home),
		state:  filepath.Join(dir, "state.json"),
		log:    filepath.Join(dir, "d.log"),
		record: filepath.Join(dir, "socket"),
	}, nil
}

func (l layout) ensureDir() error {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return fmt.Errorf("wingd: prepare %s: %w", l.dir, err)
	}
	return nil
}

// socketPathForHome maps a home to a short, collision-free socket path
// independent of the home's depth: a per-user, per-home hashed directory
// under the system socket base. Distinct homes hash to distinct
// directories, so each keeps its own daemon.
func socketPathForHome(home string) string {
	sum := sha256.Sum256([]byte(home))
	hash := hex.EncodeToString(sum[:])[:12]
	uid := os.Getuid()
	if uid < 0 {
		uid = 0
	}
	dir := filepath.Join(socketBaseDir(), fmt.Sprintf("sparkwing-%d-%s", uid, hash))
	return filepath.Join(dir, "d.sock")
}

// socketBaseDir is the short directory family unix sockets live under.
// /tmp is short and world-writable-with-sticky-bit on every unix; only
// Windows (where AF_UNIX has no sun_path limit) falls back to the
// possibly-long system temp dir.
func socketBaseDir() string {
	if runtime.GOOS == "windows" {
		return os.TempDir()
	}
	return "/tmp"
}

// maxSunPath is the OS limit on a unix socket path in bytes: 104 on
// darwin, 108 on linux and other unix. A bind past it fails with a bare
// EINVAL, so both daemon and client validate against it first and report
// the limit and path instead.
func maxSunPath() int {
	if runtime.GOOS == "darwin" {
		return 104
	}
	return 108
}

// ValidateSocketPath reports an error when sock is at or over the OS
// sun_path limit, naming both the limit and the path. Daemon bind and
// client connect both call it so an over-length path fails with a clear
// message rather than an opaque bind error.
func ValidateSocketPath(sock string) error {
	if m := maxSunPath(); len(sock) >= m {
		return fmt.Errorf("wingd: socket path %q is %d bytes, over the %d-byte OS limit; use a shorter SPARKWING_HOME", sock, len(sock), m)
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

// StateDir returns the per-home directory holding the daemon's lock,
// state file, log, and socket-pointer record. It stays under the home and
// is where discovery tools and tests look for the daemon's bookkeeping.
func StateDir(home string) (string, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return "", err
	}
	return l.dir, nil
}

// LogPath returns the daemon's log file path under home. The client
// surfaces its tail when a spawned daemon dies before serving.
func LogPath(home string) (string, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return "", err
	}
	return l.log, nil
}

// ProtocolMajor is the wire protocol major this daemon speaks; it mirrors
// [wingwire.ProtocolMajor].
const ProtocolMajor = wingwire.ProtocolMajor
