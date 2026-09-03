package wingd

import (
	"context"
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

var ErrNotElected = errors.New("wingd: another daemon is already elected")

// FinalizeDrainWindow is how long a stopping daemon waits for the run
// finalizes it started to reach the store. It is the middle of four windows
// that must nest, so a finalize is neither cut short by its own deadline
// before the drain can wait for it nor killed by the supervisor while the
// drain still holds the process open:
//
//	orchestrator.FinalizeTimeout (one finalize)
//	  < FinalizeDrainWindow (the daemon's drain)
//	  <= supervise.DefaultTermGrace (before SIGKILL)
//	  < the CLI's daemon-restart budget
const FinalizeDrainWindow = 10 * time.Second

const (
	DefaultIdleTimeout = 5 * time.Minute

	DefaultGraceWindow = 30 * time.Second

	DefaultSampleInterval = 5 * time.Second

	DefaultHeadroomMaxAge = 30 * time.Second

	DefaultCapacityInterval = 60 * time.Second

	DefaultHeadroomFraction = 0.20

	DefaultStallInterval = 10 * time.Second

	DefaultStallWindow = 60 * time.Second

	DefaultStallCPUFraction = 0.02

	DefaultStallProbeTimeout = 10 * time.Second

	staleSocketDirAge = 24 * time.Hour
)

type Config struct {
	Home string

	Version string

	HeadroomFraction float64

	Budget Budget

	BudgetSource BudgetSource
	BudgetOrigin string

	Sampler HostSampler

	ContainerRoot string

	ProcSampler ProcSampler

	SessionGuardInspector SessionGuardInspector

	GuardInterval time.Duration

	OwnedCPUSampler OwnedCPUSampler

	Now func() time.Time

	IdleTimeout time.Duration

	GraceWindow time.Duration

	SampleInterval time.Duration

	HeadroomMaxAge time.Duration

	CapacityInterval time.Duration

	StallInterval time.Duration

	StallWindow time.Duration

	StallCPUFraction float64

	StallProbeTimeout time.Duration

	// Runs is the daemon's handle on the runs store. Nil leaves every
	// store-backed behaviour off: no terminal check, no finalize.
	Runs RunStore

	// ServeAPI serves the controller HTTP API on ln until ctx ends, and
	// returns once in-flight requests have drained. The daemon binds ln
	// after it wins the election and closes it before a successor is
	// spawned, so only the election holder ever serves the API; every
	// connection ln yields has passed the peer-uid check and satisfies
	// [APIConn]. Nil leaves api.sock unbound.
	ServeAPI func(ctx context.Context, ln net.Listener)

	// StoreSchemaVersion is the runs-store schema version this daemon's
	// binary understands. It is advertised in the handshake so a newer
	// client refuses before admission instead of discovering the skew as an
	// opaque terminal-check failure.
	StoreSchemaVersion int

	// StoreRequirements names the runs-store schema requirements this
	// daemon's binary understands. It is advertised alongside
	// StoreSchemaVersion so a client refuses only when the store carries a
	// requirement this daemon lacks.
	StoreRequirements []string

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

type layout struct {
	home    string
	dir     string
	lock    string
	sock    string
	apiSock string
	state   string
	log     string
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
	sock := socketPathForHome(home)
	return layout{
		home:    home,
		dir:     dir,
		lock:    filepath.Join(dir, "d.lock"),
		sock:    sock,
		apiSock: apiSocketBeside(sock),
		state:   filepath.Join(dir, "state.json"),
		log:     filepath.Join(dir, "d.log"),
	}, nil
}

func (l layout) ensureDir() error {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return fmt.Errorf("wingd: prepare %s: %w", l.dir, err)
	}
	return nil
}

func socketPathForHome(home string) string {
	return socketPathIn(socketBaseDir(), home)
}

func socketPathIn(base, home string) string {
	sum := sha256.Sum256([]byte(home))
	hash := hex.EncodeToString(sum[:])[:12]
	return filepath.Join(base, socketDirPrefix()+hash, "d.sock")
}

func ensureSocketDir(dir string) error {
	if err := checkSocketBase(filepath.Dir(dir)); err != nil {
		return err
	}
	switch err := os.Mkdir(dir, 0o700); {
	case err == nil:
		// safety: Mkdir's mode passes through the process umask, so restate it.
		if cerr := os.Chmod(dir, 0o700); cerr != nil {
			return fmt.Errorf("wingd: restrict socket directory %s: %w", dir, cerr)
		}
		return nil
	case !errors.Is(err, fs.ErrExist):
		return fmt.Errorf("wingd: prepare socket directory %s: %w", dir, err)
	}
	return checkSocketDir(dir)
}

func checkSocketBase(base string) error {
	// safety: the base is a system path such as /tmp, which is a symlink on
	// macOS, so resolve it; the sticky bit on the target is what matters.
	info, err := os.Stat(base)
	if err != nil {
		return fmt.Errorf("wingd: inspect socket base directory %s: %w", base, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("wingd: socket base directory %s is unsafe (not a directory): %w", base, fs.ErrPermission)
	}
	if fault := socketBaseFault(info); fault != "" {
		return fmt.Errorf("wingd: socket base directory %s is unsafe (%s): %w", base, fault, fs.ErrPermission)
	}
	return nil
}

func checkSocketDir(dir string) error {
	if err := checkSocketBase(filepath.Dir(dir)); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("wingd: inspect socket directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("wingd: socket directory %s is unsafe (not a directory): %w", dir, fs.ErrPermission)
	}
	if fault := socketDirFault(info); fault != "" {
		return fmt.Errorf("wingd: socket directory %s is unsafe (%s): %w", dir, fault, fs.ErrPermission)
	}
	return nil
}

// ValidateSocketDir reports whether the directory holding sock is private to
// this user. A directory that does not exist yet is safe: whoever creates it
// creates it private.
func ValidateSocketDir(sock string) error {
	err := checkSocketDir(filepath.Dir(sock))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func socketDirPrefix() string {
	uid := os.Getuid()
	if uid < 0 {
		uid = 0
	}
	return fmt.Sprintf("sparkwing-%d-", uid)
}

func PeerSockets(home string) ([]string, error) {
	own, err := SocketPath(home)
	if err != nil {
		return nil, err
	}
	dirs, err := filepath.Glob(filepath.Join(socketBaseDir(), socketDirPrefix()+"*"))
	if err != nil {
		return nil, fmt.Errorf("wingd: scan daemon sockets: %w", err)
	}
	var peers []string
	for _, dir := range dirs {
		sock := filepath.Join(dir, "d.sock")
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
	// safety: a directory this user does not own can hold an impostor listener,
	// so leave it alone rather than opening a connection to it.
	if err := ValidateSocketDir(sock); err != nil {
		return false, false
	}
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
	if err != nil || !info.IsDir() || !socketDirReapable(info) {
		return
	}
	if _, serr := os.Lstat(sock); errors.Is(serr, fs.ErrNotExist) && time.Since(info.ModTime()) < staleSocketDirAge {
		return
	}
	_ = os.Remove(sock)
	_ = os.Remove(dir)
}

func socketBaseDir() string {
	// safety: the socket path must be a pure function of the home so every
	// caller agrees on it whatever the environment. A short shared base keeps
	// the path inside the sun_path limit; checkSocketBase and checkSocketDir
	// carry the privacy that a per-user base would otherwise provide.
	if runtime.GOOS == "windows" {
		return os.TempDir()
	}
	return "/tmp"
}

func maxSunPath() int {
	if runtime.GOOS == "darwin" {
		return 104
	}
	return 108
}

func ValidateSocketPath(sock string) error {
	if m := maxSunPath(); len(sock) >= m {
		return fmt.Errorf("wingd: socket path %q is %d bytes, over the %d-byte OS limit; use a shorter SPARKWING_HOME", sock, len(sock), m)
	}
	return nil
}

func SocketPath(home string) (string, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return "", err
	}
	return l.sock, nil
}

// safety: the admission socket is hashed into a short shared base because a
// home-relative path overruns the OS sun_path limit, and the API socket has
// to obey the same limit and the same directory privacy checks.
func apiSocketBeside(sock string) string {
	return filepath.Join(filepath.Dir(sock), "api.sock")
}

// APISocketPath reports where the daemon for home serves the controller HTTP
// API. It sits beside the admission socket, in the directory the daemon owns
// and whose privacy every caller checks with ValidateSocketDir.
func APISocketPath(home string) (string, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return "", err
	}
	return l.apiSock, nil
}

// HomeDir reports the daemon home that a caller's home argument resolves to,
// so a message can name it when the caller relied on the environment.
func HomeDir(home string) (string, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return "", err
	}
	return l.home, nil
}

func LockPath(home string) (string, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return "", err
	}
	return l.lock, nil
}

func StateDir(home string) (string, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return "", err
	}
	return l.dir, nil
}

func LogPath(home string) (string, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return "", err
	}
	return l.log, nil
}

const ProtocolMajor = wingwire.ProtocolMajor

const MinProtocolMajor = wingwire.MinProtocolMajor
