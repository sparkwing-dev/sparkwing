// Package crontimer installs the per-user OS timer that runs
// `sparkwing __tick --home <home>` every minute for one Sparkwing home: a systemd user
// timer on Linux, a launchd agent on macOS. A single timer serves every armed
// schedule in that home because sparkwing evaluates the cron expressions itself.
//
// Every managed file carries [Marker], so status and uninstall can tell a
// file sparkwing wrote from one a person wrote by hand; a file without the
// marker is never overwritten or removed. All contact with the machine --
// systemctl, launchctl, the home and config directories, the binary path --
// arrives through [Host], so a test decides what the machine looks like and
// both platforms are exercised anywhere.
package crontimer

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

// Marker appears verbatim in every file this package writes.
const Marker = "Installed by sparkwing"

// ErrUnsupported is returned, wrapped, for a GOOS that has no cron timer.
var ErrUnsupported = errors.New("no sparkwing cron timer on this operating system")

// Host is everything the installer needs to know about the machine.
type Host struct {
	// GOOS is "linux" or "darwin"; anything else yields [ErrUnsupported].
	GOOS string

	// Home is the user's home directory, where launchd reads LaunchAgents.
	Home string

	// ConfigHome is $XDG_CONFIG_HOME or ~/.config, where systemd reads user units.
	ConfigHome string

	// Binary is the absolute path of the sparkwing binary the timer runs.
	Binary string

	// Version is recorded in the managed file for doctor and software updates.
	Version string

	// PathEnv is the PATH baked into the unit or plist. A launchd or systemd
	// job inherits almost nothing, so the installer's own PATH is what makes
	// git, docker and the rest reachable from the tick.
	PathEnv string

	// Env carries extra environment into the tick, on top of PATH.
	Env map[string]string

	// SparkwingHome is the Sparkwing home whose schedules the tick fires. The
	// timer carries it in --home, and a home other than ~/.sparkwing
	// gets a timer name of its own, so installing from one home never replaces
	// another home's timer.
	SparkwingHome string

	// LogPath receives the tick's stdout and stderr, appended.
	LogPath string

	// UID names the launchd domain gui/<uid>.
	UID int

	// Exec runs systemctl or launchctl and returns stdout and stderr combined.
	Exec func(context.Context, string, ...string) (string, error)
}

// State describes the timer as it exists on the host.
type State struct {
	Arguments []string `json:"arguments,omitempty"`
	Home      string   `json:"home,omitempty"`
	Version   string   `json:"version,omitempty"`
	// Installed reports a managed unit or plist at the expected path.
	Installed bool `json:"installed"`

	// Foreign reports a file at that path without [Marker]. Such a file is
	// never overwritten and never removed.
	Foreign bool `json:"foreign"`

	// Enabled reports that the OS will fire the timer: on systemd the timer is
	// enabled and active, on launchd the job is loaded.
	Enabled bool `json:"enabled"`

	// Path is the unit or plist path; on systemd it is the .timer.
	Path string `json:"path"`

	// Binary is the binary the managed file runs, read back out of the file
	// itself, or empty when no managed file was parseable.
	Binary string `json:"binary,omitempty"`

	// Stale reports that Binary is not the binary the caller asked for, which
	// happens when sparkwing is reinstalled somewhere else.
	Stale bool `json:"stale"`

	// Detail is one sentence for a human.
	Detail string `json:"detail,omitempty"`
}

// Install writes the timer's files, enables it, and reports the state that
// follows. A foreign file at either path is an error and nothing is written.
// When enabling fails the files stay on disk and the returned State says so,
// alongside the error.
func Install(ctx context.Context, h Host) (State, error) {
	if err := checkOperation(ctx, h); err != nil {
		return State{}, err
	}
	if h.GOOS == "linux" || h.GOOS == "darwin" {
		if err := requireBinary(h); err != nil {
			return State{}, err
		}
	}
	switch h.GOOS {
	case "linux":
		return installLinux(ctx, h)
	case "darwin":
		return installDarwin(ctx, h)
	}
	return State{}, unsupported(h.GOOS)
}

// Uninstall disables the timer and removes the files sparkwing wrote. A
// foreign file is left in place and reported through State.Foreign.
func Uninstall(ctx context.Context, h Host) (State, error) {
	if err := checkOperation(ctx, h); err != nil {
		return State{}, err
	}
	switch h.GOOS {
	case "linux":
		return uninstallLinux(ctx, h)
	case "darwin":
		return uninstallDarwin(ctx, h)
	}
	return State{}, unsupported(h.GOOS)
}

// Status reports the timer without changing anything. A service manager that
// cannot be reached -- no user session under a bare WSL, no launchd domain --
// leaves Enabled false and says why in Detail; it is not an error, because the
// files on disk still answer the question that was asked.
func Status(ctx context.Context, h Host) (State, error) {
	if err := checkOperation(ctx, h); err != nil {
		return State{}, err
	}
	switch h.GOOS {
	case "linux":
		return statusLinux(ctx, h)
	case "darwin":
		return statusDarwin(ctx, h)
	}
	return State{}, unsupported(h.GOOS)
}

// DefaultExec runs a command and returns its output, stdout and stderr
// combined, for a Host that talks to the real machine.
func DefaultExec(ctx context.Context, name string, args ...string) (string, error) {
	out, err := procenv.HostCommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func unsupported(goos string) error {
	if goos == "" {
		goos = "unknown"
	}
	return fmt.Errorf("crontimer: %s: %w", goos, ErrUnsupported)
}

func (h Host) run(ctx context.Context, name string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if h.Exec == nil {
		return "", errors.New("crontimer: Host.Exec is required")
	}
	return h.Exec(ctx, name, args...)
}

type managed struct {
	path   string
	exists bool
	ours   bool
	body   string
}

func readManaged(path string) (managed, error) {
	body, exists, ours, err := unitfile.Read(path, Marker)
	return managed{path: path, body: body, exists: exists, ours: ours}, err
}

func writeManaged(path, body string) error {
	return unitfile.Write(path, body)
}

// safety: systemd's append: and launchd's StandardOutPath create the log file but not its directory.
func ensureLogDir(logPath string) error {
	if logPath == "" {
		return nil
	}
	dir := filepath.Dir(logPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("crontimer: create %s: %w", dir, err)
	}
	return nil
}

func removeManaged(files ...managed) error {
	for _, f := range files {
		if !f.exists || !f.ours {
			continue
		}
		if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("crontimer: remove %s: %w", f.path, err)
		}
	}
	return nil
}

// safety: a repeat uninstall finds nothing loaded, and that complaint is not a failure.
func notLoaded(out string) bool {
	low := strings.ToLower(out)
	for _, phrase := range []string{
		"not loaded",
		"no such file",
		"no such process",
		"could not find specified service",
		"does not exist",
		"not found",
	} {
		if strings.Contains(low, phrase) {
			return true
		}
	}
	return false
}

func (h Host) instance() string {
	return unitfile.Instance(h.Home, h.SparkwingHome)
}

func otherHomeDetail(h Host, path string) string {
	return fmt.Sprintf("%s ticks another Sparkwing home, not %s; inspect the unit before reinstalling schedules",
		path, h.SparkwingHome)
}

// safety: merging and sorting keep the generated file byte-identical across runs.
func envPairs(h Host) [][2]string {
	merged := make(map[string]string, len(h.Env)+2)
	if h.PathEnv != "" {
		merged["PATH"] = h.PathEnv
	}
	for k, v := range h.Env {
		if k == "" {
			continue
		}
		merged[k] = v
	}
	pairs := make([][2]string, 0, len(merged))
	for _, k := range slices.Sorted(maps.Keys(merged)) {
		pairs = append(pairs, [2]string{k, merged[k]})
	}
	return pairs
}

func requireBinary(h Host) error {
	if h.Version == "" || strings.ContainsAny(h.Version, "\r\n<>\x00") {
		return errors.New("crontimer: Host.Version is required and must be one line")
	}
	if h.Binary == "" {
		return errors.New("crontimer: Host.Binary is required")
	}
	if !filepath.IsAbs(h.Binary) {
		return fmt.Errorf("crontimer: Host.Binary must be absolute, got %q", h.Binary)
	}
	return nil
}

func staleAgainst(h Host, binary string) bool {
	return binary != "" && h.Binary != "" && binary != h.Binary
}
