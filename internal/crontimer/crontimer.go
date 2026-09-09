// Package crontimer installs the one per-user OS timer that runs
// `sparkwing crons tick` every minute: a systemd user timer on Linux, a
// launchd agent on macOS. A single timer serves every armed schedule on the
// host because sparkwing evaluates the cron expressions itself.
//
// Every managed file carries [Marker], so status and uninstall can tell a
// file sparkwing wrote from one a person wrote by hand; a file without the
// marker is never overwritten or removed. All contact with the machine --
// systemctl, launchctl, the home and config directories, the binary path --
// arrives through [Host], so a test decides what the machine looks like and
// both platforms are exercised anywhere.
package crontimer

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Marker appears verbatim in every file this package writes.
const Marker = "Installed by sparkwing"

// ErrUnsupported is returned, wrapped, for a GOOS that has no cron timer.
var ErrUnsupported = errors.New("no sparkwing cron timer on this operating system")

// hack: 0644, not the runner installer's 0600 -- a unit or plist carries no token.
const fileMode os.FileMode = 0o644

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

	// PathEnv is the PATH baked into the unit or plist. A launchd or systemd
	// job inherits almost nothing, so the installer's own PATH is what makes
	// git, docker and the rest reachable from the tick.
	PathEnv string

	// Env carries extra environment into the tick, on top of PATH.
	Env map[string]string

	// LogPath receives the tick's stdout and stderr, appended.
	LogPath string

	// UID names the launchd domain gui/<uid>.
	UID int

	// Exec runs systemctl or launchctl and returns stdout and stderr combined.
	Exec func(name string, args ...string) (string, error)
}

// State describes the timer as it exists on the host.
type State struct {
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
func Install(h Host) (State, error) {
	switch h.GOOS {
	case "linux":
		return installLinux(h)
	case "darwin":
		return installDarwin(h)
	}
	return State{}, unsupported(h.GOOS)
}

// Uninstall disables the timer and removes the files sparkwing wrote. A
// foreign file is left in place and reported through State.Foreign.
func Uninstall(h Host) (State, error) {
	switch h.GOOS {
	case "linux":
		return uninstallLinux(h)
	case "darwin":
		return uninstallDarwin(h)
	}
	return State{}, unsupported(h.GOOS)
}

// Status reports the timer without changing anything. A service manager that
// cannot be reached -- no user session under a bare WSL, no launchd domain --
// leaves Enabled false and says why in Detail; it is not an error, because the
// files on disk still answer the question that was asked.
func Status(h Host) (State, error) {
	switch h.GOOS {
	case "linux":
		return statusLinux(h)
	case "darwin":
		return statusDarwin(h)
	}
	return State{}, unsupported(h.GOOS)
}

// DefaultExec runs a command and returns its output, stdout and stderr
// combined, for a Host that talks to the real machine.
func DefaultExec(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func unsupported(goos string) error {
	if goos == "" {
		goos = "unknown"
	}
	return fmt.Errorf("crontimer: %s: %w", goos, ErrUnsupported)
}

func (h Host) run(name string, args ...string) (string, error) {
	if h.Exec == nil {
		return "", errors.New("crontimer: Host.Exec is required")
	}
	return h.Exec(name, args...)
}

type managed struct {
	path   string
	exists bool
	ours   bool
	body   string
}

func readManaged(path string) (managed, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return managed{path: path}, nil
	}
	if err != nil {
		return managed{path: path}, fmt.Errorf("crontimer: read %s: %w", path, err)
	}
	return managed{path: path, exists: true, ours: strings.Contains(string(body), Marker), body: string(body)}, nil
}

func writeManaged(path, body string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("crontimer: create %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".crontimer-*.tmp")
	if err != nil {
		return fmt.Errorf("crontimer: create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() {
		if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "sparkwing: could not clear the temporary file %s: %v\n", tmp, err)
		}
	}()
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("crontimer: write %s: %w", tmp, err)
	}
	if err := f.Chmod(fileMode); err != nil {
		_ = f.Close()
		return fmt.Errorf("crontimer: chmod %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("crontimer: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("crontimer: rename %s to %s: %w", tmp, path, err)
	}
	return nil
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

// safety: merging and sorting keep the generated file byte-identical across runs.
func envPairs(h Host) [][2]string {
	merged := make(map[string]string, len(h.Env)+1)
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
