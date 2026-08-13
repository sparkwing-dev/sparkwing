// Package wingd implements sparkwingd, the single local admission
// arbiter. One daemon per sparkwing home owns the admission ledger
// (internal/admission) and serves clients over a unix socket speaking the
// wingwire protocol (pkg/wingwire). Ordinary leases derive liveness from the
// socket. A lease guarding an external process session instead survives its
// supervisor connection and remains held until the kernel reports that exact
// session empty.
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
// Restored grants remain authoritative when the current budget is smaller;
// new admission tightens while those holders reattach or drain. A structurally
// invalid legacy snapshot is quarantined. An
// unreadable state file or unknown schema prevents startup because it may
// contain guarded process authority; serving from an empty ledger could admit
// overlapping work.
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
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
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
	DefaultGraceWindow = 30 * time.Second
	// DefaultSampleInterval is the period between host-load samples.
	DefaultSampleInterval = 5 * time.Second
	// DefaultHeadroomMaxAge bounds how long hysteresis may hold an applied
	// host-pressure value before the newest successful sample is re-applied.
	DefaultHeadroomMaxAge = 30 * time.Second
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
	// DefaultStallProbeTimeout bounds how long an idle-looking holder has to
	// answer a control-plane liveness challenge before its lease is reclaimed.
	DefaultStallProbeTimeout = 10 * time.Second
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
	// BudgetSource and BudgetOrigin record where Budget was resolved
	// from, for the queue and doctor views to report. Both come from
	// [ResolveBudget]; a caller that builds a Budget itself leaves them
	// empty and the daemon reports the source as unknown rather than
	// guessing one.
	BudgetSource BudgetSource
	BudgetOrigin string
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
	// SessionGuardInspector validates and controls exact process sessions for
	// guarded admission. Nil uses the kernel-backed platform implementation.
	SessionGuardInspector SessionGuardInspector
	// GuardInterval controls how quickly an orphaned guarded session is
	// observed empty. Zero uses the default.
	GuardInterval time.Duration
	// OwnedCPUSampler reads the combined CPU usage of live holder process
	// trees for external-load accounting. Nil uses the platform sampler.
	// An explicit value uses separate host and owned readings; New rejects
	// a Sampler that also provides paired owned CPU accounting.
	OwnedCPUSampler OwnedCPUSampler
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
	// HeadroomMaxAge overrides [DefaultHeadroomMaxAge] when non-zero. It
	// bounds deadband hysteresis; host measurements still run at
	// SampleInterval.
	HeadroomMaxAge time.Duration
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
	// StallProbeTimeout overrides [DefaultStallProbeTimeout] when non-zero.
	StallProbeTimeout time.Duration
	// FinalizeRun, when set, is called with a run ID whose client
	// disconnected while still holding or awaiting admission -- the
	// process died without releasing (SIGKILL, panic). The callee
	// finalizes the orphaned run row so it does not sit in a running
	// state forever; it must tolerate rows that are already terminal or
	// absent. Called on its own goroutine, never under daemon locks.
	FinalizeRun func(runID string)
	// FinalizeCancelledRuns atomically records every run sharing a cancelled
	// lease before the daemon acknowledges or signals the cancellation.
	FinalizeCancelledRuns func(runIDs []string, reason string) error
	// IsRunTerminal checks the durable run authority before admitting an ID
	// that is not present in the daemon's bounded cancellation cache.
	IsRunTerminal func(runID string) (bool, error)
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

func (c Config) guardInterval() time.Duration {
	if c.GuardInterval > 0 {
		return c.GuardInterval
	}
	return defaultGuardInterval
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

func (c Config) headroomMaxAge() time.Duration {
	if c.HeadroomMaxAge > 0 {
		return c.HeadroomMaxAge
	}
	return DefaultHeadroomMaxAge
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

func (c Config) stallProbeTimeout() time.Duration {
	if c.StallProbeTimeout > 0 {
		return c.StallProbeTimeout
	}
	return DefaultStallProbeTimeout
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
// The lock, state, and log live under the home directory, which has no
// length limit; only the socket itself is placed
// on a short hashed path so a deep home cannot push it past the OS
// sun_path limit and break bind.
type layout struct {
	dir   string
	lock  string
	sock  string
	state string
	log   string
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
		sock:  socketPathForHome(home),
		state: filepath.Join(dir, "state.json"),
		log:   filepath.Join(dir, "d.log"),
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
	dir := filepath.Join(socketBaseDir(), socketDirPrefix()+hash)
	return filepath.Join(dir, "d.sock")
}

// socketDirPrefix is the leading, home-independent part of a socket
// directory's name: the family and this user, so one user's daemons never
// collide with another's and every one of a user's daemons is findable
// from the prefix alone.
func socketDirPrefix() string {
	uid := os.Getuid()
	if uid < 0 {
		uid = 0
	}
	return fmt.Sprintf("sparkwing-%d-", uid)
}

// PeerSockets returns the socket paths of this user's daemons for every
// sparkwing home other than the given one. A home's daemon is reachable
// only at that home's own socket, so a tool inspecting one home is blind
// to a daemon serving another until it looks here.
//
// Dead socket entries owned by this user are removed during the sweep, so
// callers see only listeners that answered a connection attempt.
func PeerSockets(home string) ([]string, error) {
	own, err := SocketPath(home)
	if err != nil {
		return nil, err
	}
	matches, err := filepath.Glob(filepath.Join(socketBaseDir(), socketDirPrefix()+"*", "d.sock"))
	if err != nil {
		return nil, fmt.Errorf("wingd: scan daemon sockets: %w", err)
	}
	peers := make([]string, 0, len(matches))
	for _, sock := range matches {
		alive, dead := socketStatus(sock)
		if dead {
			reapSocketDir(sock)
			continue
		}
		if alive && sock != own {
			peers = append(peers, sock)
		}
	}
	return peers, nil
}

func socketStatus(sock string) (alive, dead bool) {
	info, err := os.Lstat(sock)
	if err != nil {
		return false, errors.Is(err, fs.ErrNotExist)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return false, true
	}
	c, err := net.DialTimeout("unix", sock, 100*time.Millisecond)
	if err != nil {
		return false, socketDialMeansDead(err)
	}
	_ = c.Close()
	return true, false
}

func socketDialMeansDead(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, fs.ErrNotExist)
}

func reapSocketDir(sock string) {
	dir := filepath.Dir(sock)
	if filepath.Base(sock) != "d.sock" || !strings.HasPrefix(filepath.Base(dir), socketDirPrefix()) {
		return
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return
	}
	_ = os.Remove(sock)
	_ = os.Remove(dir)
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

// StateDir returns the per-home directory holding the daemon's lock, state
// file, and log.
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

// ProtocolMajor is the newest wire protocol major this daemon speaks; it
// mirrors [wingwire.ProtocolMajor].
const ProtocolMajor = wingwire.ProtocolMajor

// MinProtocolMajor is the oldest wire protocol major this daemon still
// serves; it mirrors [wingwire.MinProtocolMajor].
const MinProtocolMajor = wingwire.MinProtocolMajor
