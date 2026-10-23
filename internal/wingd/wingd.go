package wingd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

var ErrNotElected = errors.New("wingd: another daemon is already elected")

// FinalizeDrainWindow bounds admission's warning before the local runtime joins finalizers.
const FinalizeDrainWindow = 10 * time.Second

const (
	DefaultIdleTimeout = limits.LocalControllerIdleTimeout

	DefaultGraceWindow = 2 * time.Minute

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

	Budget userconfig.Budget

	BudgetSource wingwire.BudgetSource
	BudgetOrigin string

	AdmissionPolicy *AdmissionPolicy

	Sampler HostSampler

	ContainerRoot string

	ProcSampler ProcSampler

	OwnedCPUSampler OwnedCPUSampler

	Now func() time.Time

	Stay bool

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
	// store-backed behavior off: no terminal check, no finalize.
	Runs RunStore

	// ServeAPI serves the elected controller over connections with verified UID or SID identity.
	ServeAPI func(ctx context.Context, ln net.Listener)

	// StoreSchemaVersion is the runs-store schema version this daemon's
	// binary understands. It is advertised in the handshake so a newer
	// client refuses before admission instead of discovering the skew as an
	// opaque terminal-check failure.
	StoreSchemaVersion int

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
	start   string
	sock    string
	apiSock string
	state   string
	log     string
}

func resolveLayout(home string) (layout, error) { return layoutFor(home, false) }

// safety: only a starting daemon or its starter records the socket id, so a
// path lookup never initializes daemon state.
func daemonLayout(home string) (layout, error) { return layoutFor(home, true) }

func layoutFor(home string, create bool) (layout, error) {
	if home == "" {
		p, err := paths.DefaultPaths()
		if err != nil {
			return layout{}, fmt.Errorf("wingd: resolve home: %w", err)
		}
		home = p.Root
	}
	home, key, err := socketHomeIdentity(home)
	if err != nil {
		return layout{}, fmt.Errorf("wingd: resolve home identity: %w", err)
	}
	dir := filepath.Join(home, "wingd")
	sock, err := socketPathForHome(dir, key, create)
	if err != nil {
		return layout{}, err
	}
	return layout{
		home:    home,
		dir:     dir,
		lock:    filepath.Join(dir, "d.lock"),
		start:   filepath.Join(dir, "d.start.lock"),
		sock:    sock,
		apiSock: APISocketBeside(sock),
		state:   filepath.Join(dir, "state.json"),
		log:     filepath.Join(dir, "d.log"),
	}, nil
}

func (l layout) ensureDir() error {
	if err := fssecure.EnsureDir(l.dir); err != nil {
		return fmt.Errorf("wingd: prepare %s: %w", l.dir, err)
	}
	return nil
}

func socketPathForHome(dir, home string, create bool) (string, error) {
	base, err := socketBaseDir()
	if err != nil {
		return "", err
	}
	token, err := readSocketToken(filepath.Join(dir, socketTokenFile))
	if errors.Is(err, fs.ErrNotExist) && !create {
		return filepath.Join(dir, unboundSocketDir, "d.sock"), nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		token, err = recordSocketToken(dir)
	}
	if err != nil {
		return "", err
	}
	return socketPathIn(base, home, token), nil
}

func socketPathIn(base, home, token string) string {
	sum := sha256.Sum256([]byte(home))
	hash := hex.EncodeToString(sum[:])[:12]
	return filepath.Join(base, socketDirPrefix()+hash+"-"+token, "d.sock")
}

var errUnsafeSocketDir = fmt.Errorf("unsafe socket directory: %w", fs.ErrPermission)

const (
	socketTokenFile  = "socket-id"
	unboundSocketDir = "unbound"
)

var socketTokenPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// safety: a socket directory name derived from the home alone lets another
// account create it first in the shared base and lock this user out.
func recordSocketToken(dir string) (string, error) { return writeSocketToken(dir, false) }

// safety: an elected daemon replaces an id whose directory another account
// planted, so a squatted name delays a start instead of blocking it.
func rotateSocketToken(dir string) error {
	_, err := writeSocketToken(dir, true)
	return err
}

func writeSocketToken(dir string, replace bool) (string, error) {
	path := filepath.Join(dir, socketTokenFile)
	if err := fssecure.EnsureDir(dir); err != nil {
		return "", fmt.Errorf("wingd: prepare %s: %w", dir, err)
	}
	draft, err := os.CreateTemp(dir, socketTokenFile+".*")
	if err != nil {
		return "", fmt.Errorf("wingd: record socket id: %w", err)
	}
	defer os.Remove(draft.Name())
	var id [8]byte
	_, _ = rand.Read(id[:])
	_, werr := draft.WriteString(hex.EncodeToString(id[:]))
	if err := errors.Join(werr, draft.Close()); err != nil {
		return "", fmt.Errorf("wingd: record socket id: %w", err)
	}
	publish := os.Link
	if replace {
		publish = os.Rename
	}
	if err := publish(draft.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("wingd: record socket id: %w", err)
	}
	return readSocketToken(path)
}

func readSocketToken(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if token := strings.TrimSpace(string(raw)); socketTokenPattern.MatchString(token) {
		return token, nil
	}
	return "", fmt.Errorf("wingd: socket id %s is malformed; remove it to choose a new one", path)
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
		return fmt.Errorf("wingd: socket directory %s is unsafe (not a directory): %w", dir, errUnsafeSocketDir)
	}
	if fault := socketDirFault(info); fault != "" {
		return fmt.Errorf("wingd: socket directory %s is unsafe (%s): %w", dir, fault, errUnsafeSocketDir)
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

func maxSunPath() int {
	if runtime.GOOS == "darwin" {
		return 104
	}
	return 108
}

func ValidateSocketPath(sock string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if m := maxSunPath(); len(sock) >= m {
		return fmt.Errorf("wingd: socket path %q is %d bytes, over the %d-byte OS limit; use a shorter SPARKWING_HOME", sock, len(sock), m)
	}
	return nil
}

// EnsureSocketPath reports the admission socket for home, recording the
// home's socket id first when no daemon has bound one.
func EnsureSocketPath(home string) (string, error) {
	l, err := daemonLayout(home)
	if err != nil {
		return "", err
	}
	return l.sock, nil
}

func SocketPath(home string) (string, error) {
	l, err := resolveLayout(home)
	if err != nil {
		return "", err
	}
	return l.sock, nil
}

// APISocketBeside reports the controller API socket that belongs to the
// admission socket at sock. A caller that already holds a daemon connection
// derives the path from the socket it reached rather than recomputing the
// home hash.
//
// safety: the admission socket is hashed into a short shared base because a
// home-relative path overruns the OS sun_path limit, and the API socket has
// to obey the same limit and the same directory privacy checks.
func APISocketBeside(sock string) string {
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
