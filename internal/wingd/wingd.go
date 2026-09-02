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

var ErrNotElected = errors.New("wingd: another daemon is already elected")

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

	FinalizeRun func(runID string)

	FinalizeCancelledRuns func(runIDs []string, reason string) error

	IsRunTerminal func(runID string) (bool, error)

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
	home  string
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
		home:  home,
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

func socketPathForHome(home string) string {
	return socketPathIn(socketBaseDir(), home)
}

func socketPathIn(base, home string) string {
	sum := sha256.Sum256([]byte(home))
	hash := hex.EncodeToString(sum[:])[:12]
	return filepath.Join(base, socketDirPrefix()+hash, "d.sock")
}

func ensureSocketDir(dir string) error {
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

func checkSocketDir(dir string) error {
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
	var peers []string
	for _, base := range socketBaseDirs() {
		matches, err := filepath.Glob(filepath.Join(base, socketDirPrefix()+"*", "d.sock"))
		if err != nil {
			return nil, fmt.Errorf("wingd: scan daemon sockets: %w", err)
		}
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
	if err != nil || !info.IsDir() || socketDirFault(info) != "" {
		return
	}
	_ = os.Remove(sock)
	_ = os.Remove(dir)
}

func socketBaseDir() string {
	// safety: a per-user runtime directory cannot be pre-created by another
	// account; the shared temp directory is the length-limited fallback.
	if base := runtimeSocketBaseDir(); base != "" && ValidateSocketPath(socketPathIn(base, "")) == nil {
		return base
	}
	return tempSocketBaseDir()
}

func runtimeSocketBaseDir() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || socketDirFault(info) != "" {
		return ""
	}
	return dir
}

func tempSocketBaseDir() string {
	if runtime.GOOS == "windows" {
		return os.TempDir()
	}
	return "/tmp"
}

func socketBaseDirs() []string {
	base := socketBaseDir()
	if legacy := tempSocketBaseDir(); legacy != base {
		return []string{base, legacy}
	}
	return []string{base}
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
	return servedSocketPath(l), nil
}

func servedSocketPath(l layout) string {
	// safety: an upgrade can move the base directory while a daemon of the
	// previous build still serves the old path; prefer a bound socket over a
	// derived one so clients keep reaching the daemon that holds the election.
	legacy := socketPathIn(tempSocketBaseDir(), l.home)
	if legacy == l.sock || boundSocket(l.sock) || !boundSocket(legacy) {
		return l.sock
	}
	return legacy
}

func boundSocket(sock string) bool {
	info, err := os.Lstat(sock)
	return err == nil && info.Mode()&os.ModeSocket != 0
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
