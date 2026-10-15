// Package service installs and refreshes the runner's per-user systemd service
// or launchd agent. Units run sparkwing-runner start against an existing machine
// configuration. Files without Marker remain untouched. Refresh and Uninstall
// also preserve registrations for another configuration.
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runners/runnerflags"
	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

const (
	// Marker appears verbatim in every file this package writes.
	Marker = "Installed by sparkwing"

	// ServiceName is the systemd user unit that runs the runner.
	ServiceName = "sparkwing-runner.service"

	// Label is the launchd label of the runner.
	Label = "com.sparkwing.runner"

	// PlistName is the file launchd reads the runner from.
	PlistName = Label + ".plist"
)

// ErrUnsupported is returned, wrapped, for a GOOS that has no runner service.
var ErrUnsupported = errors.New("no sparkwing runner service on this operating system")

// Host is everything the installer needs to know about the machine.
type Host struct {
	// GOOS is "linux" or "darwin"; anything else yields [ErrUnsupported].
	GOOS string

	// Home is the user's home directory, where launchd reads LaunchAgents.
	Home string

	// ConfigHome is $XDG_CONFIG_HOME or ~/.config, where systemd reads user units.
	ConfigHome string

	// Binary is the absolute path of the sparkwing-runner binary.
	Binary string

	// ConfigPath is the config.yaml whose runner section the service reads its
	// credential-file location from.
	ConfigPath string

	// LogPath receives the runner's stdout and stderr on macOS. systemd
	// journals the unit instead.
	LogPath string

	// UID names the launchd domain gui/<uid>.
	UID int

	// Exec runs systemctl or launchctl and returns stdout and stderr combined.
	Exec func(context.Context, string, ...string) (string, error)
}

// State describes the service as it exists on the host.
type State struct {
	// Installed reports a managed unit or plist at the expected path.
	Installed bool `json:"installed"`

	// Foreign reports a file without Marker. It is never overwritten or removed.
	Foreign bool `json:"foreign"`

	// Mismatch reports a managed file that runs a different runner config than
	// the one [Host] names. [Uninstall] leaves such a file in place.
	Mismatch bool `json:"mismatch"`

	// Running reports that the service manager accepted the start or stop.
	Running bool `json:"running"`

	// Path is the unit or plist path.
	Path string `json:"path"`

	// Binary and Arguments are read from the installed unit, without opening configuration.
	Binary    string   `json:"binary,omitempty"`
	Arguments []string `json:"arguments,omitempty"`

	// Detail is one sentence for a human.
	Detail string `json:"detail,omitempty"`
}

// Install writes the service file, starts it, and reports the state that
// follows. A foreign file is an error and nothing is written.
func Install(ctx context.Context, h Host) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	switch h.GOOS {
	case "linux":
		return installLinux(ctx, h)
	case "darwin":
		return installDarwin(ctx, h)
	}
	return State{}, unsupported(h.GOOS)
}

// Uninstall stops the service and removes the file sparkwing wrote. A foreign
// file is left in place and reported through State.Foreign.
func Uninstall(ctx context.Context, h Host) (State, error) {
	if err := ctx.Err(); err != nil {
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

// Preflight reports whether this machine can run the service at all: the
// binary and config paths resolve, and the service manager answers. Call it
// before doing anything expensive or irreversible, so a host that cannot
// supervise a runner fails before the work rather than after it.
func Preflight(ctx context.Context, h Host) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch h.GOOS {
	case "linux":
		if err := requireHost(h); err != nil {
			return err
		}
		if h.ConfigHome == "" {
			return errors.New("runner service: Host.ConfigHome is required on linux")
		}
		if out, err := h.run(ctx, "systemctl", "--user", "show", "-p", "Version", "--value"); err != nil {
			return fmt.Errorf("runner service: systemd user session is unreachable (systemctl --user show): %w: %s",
				err, strings.TrimSpace(out))
		}
		return nil
	case "darwin":
		if err := requireHost(h); err != nil {
			return err
		}
		if h.Home == "" {
			return errors.New("runner service: Host.Home is required on darwin")
		}
		if out, err := h.run(ctx, "launchctl", "print", h.domainTarget()); err != nil {
			return fmt.Errorf("runner service: launchd domain %s is unreachable (launchctl print): %w: %s",
				h.domainTarget(), err, strings.TrimSpace(out))
		}
		return nil
	}
	return unsupported(h.GOOS)
}

// DefaultExec runs a command and returns its output, stdout and stderr
// combined, for a Host that talks to the real machine.
func DefaultExec(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// UnitPath reports the systemd user unit path for a host.
func UnitPath(h Host) string {
	return filepath.Join(h.ConfigHome, "systemd", "user", ServiceName)
}

// PlistPath reports the LaunchAgent path for a host.
func PlistPath(h Host) string {
	return filepath.Join(h.Home, "Library", "LaunchAgents", PlistName)
}

func installLinux(ctx context.Context, h Host) (State, error) {
	if err := requireHost(h); err != nil {
		return State{}, err
	}
	if h.ConfigHome == "" {
		return State{}, errors.New("runner service: Host.ConfigHome is required on linux")
	}
	path := UnitPath(h)
	existing, err := readManaged(path)
	if err != nil {
		return State{}, err
	}
	if existing.foreign() {
		return foreignState(path), foreignError(path)
	}
	if err := unitfile.CheckSystemdSource(ctx, h.Exec, ServiceName, path); err != nil {
		return State{}, err
	}
	if err := writeManaged(path, serviceUnit(h)); err != nil {
		return State{Path: path}, err
	}

	state := State{Installed: true, Path: path}
	if out, err := h.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		state.Detail = "the unit is written but systemd did not reload it"
		return state, fmt.Errorf("runner service: systemctl --user daemon-reload: %w: %s", err, strings.TrimSpace(out))
	}
	if out, err := h.run(ctx, "systemctl", "--user", "enable", "--now", ServiceName); err != nil {
		state.Detail = "the unit is written but systemd did not start it"
		return state, fmt.Errorf("runner service: systemctl --user enable --now %s: %w: %s", ServiceName, err, strings.TrimSpace(out))
	}
	state.Running = true
	state.Detail = fmt.Sprintf("%s is enabled and running %s start", ServiceName, h.Binary)
	return state, nil
}

func uninstallLinux(ctx context.Context, h Host) (State, error) {
	if h.ConfigHome == "" {
		return State{}, errors.New("runner service: Host.ConfigHome is required on linux")
	}
	path := UnitPath(h)
	existing, err := readManaged(path)
	if err != nil {
		return State{}, err
	}
	if existing.foreign() {
		return foreignState(path), nil
	}
	if !existing.exists {
		return State{Path: path, Detail: "no sparkwing runner service is installed here"}, nil
	}
	if other := unitConfigPath(existing.body); !sameConfig(other, h.ConfigPath) {
		return mismatchState(path, other, h.ConfigPath), nil
	}
	if err := unitfile.CheckSystemdSource(ctx, h.Exec, ServiceName, path); err != nil {
		return State{}, err
	}

	var stopErr error
	if out, err := h.run(ctx, "systemctl", "--user", "disable", "--now", ServiceName); err != nil && !notLoaded(out) {
		stopErr = fmt.Errorf("runner service: systemctl --user disable --now %s: %w: %s", ServiceName, err, strings.TrimSpace(out))
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{Path: path}, errors.Join(stopErr, fmt.Errorf("runner service: remove %s: %w", path, err))
	}
	state := State{Path: path, Detail: "the sparkwing runner service is stopped and removed"}
	if out, err := h.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return state, errors.Join(stopErr, fmt.Errorf("runner service: systemctl --user daemon-reload: %w: %s", err, strings.TrimSpace(out)))
	}
	if stopErr != nil {
		state.Detail = "the unit is removed but systemd reported an error stopping it"
	}
	return state, stopErr
}

func installDarwin(ctx context.Context, h Host) (State, error) {
	if err := requireHost(h); err != nil {
		return State{}, err
	}
	if h.Home == "" {
		return State{}, errors.New("runner service: Host.Home is required on darwin")
	}
	path := PlistPath(h)
	existing, err := readManaged(path)
	if err != nil {
		return State{}, err
	}
	if existing.foreign() {
		return foreignState(path), foreignError(path)
	}
	loaded, _, err := agentRegistration(ctx, h)
	if err != nil {
		return State{}, err
	}
	if err := ensureLogDir(h.LogPath); err != nil {
		return State{Path: path}, err
	}
	if err := writeManaged(path, agentPlist(h)); err != nil {
		return State{Path: path}, err
	}

	state := State{Installed: true, Path: path}
	if loaded {
		if err := runRefresh(ctx, h, "launchctl", "bootout", h.serviceTarget()); err != nil {
			return state, err
		}
	}
	if out, err := h.run(ctx, "launchctl", "bootstrap", h.domainTarget(), path); err != nil {
		state.Detail = "the plist is written but launchd did not load it"
		return state, fmt.Errorf("runner service: launchctl bootstrap %s %s: %w: %s",
			h.domainTarget(), path, err, strings.TrimSpace(out))
	}
	state.Running = true
	state.Detail = fmt.Sprintf("%s is loaded and running %s start", Label, h.Binary)
	return state, nil
}

func uninstallDarwin(ctx context.Context, h Host) (State, error) {
	if h.Home == "" {
		return State{}, errors.New("runner service: Host.Home is required on darwin")
	}
	path := PlistPath(h)
	existing, err := readManaged(path)
	if err != nil {
		return State{}, err
	}
	if existing.foreign() {
		return foreignState(path), nil
	}
	if !existing.exists {
		return State{Path: path, Detail: "no sparkwing runner service is installed here"}, nil
	}
	if other := plistConfigPath([]byte(existing.body)); !sameConfig(other, h.ConfigPath) {
		return mismatchState(path, other, h.ConfigPath), nil
	}

	loaded, _, err := agentRegistration(ctx, h)
	if err != nil {
		return State{}, err
	}
	var stopErr error
	if loaded {
		stopErr = runRefresh(ctx, h, "launchctl", "bootout", h.serviceTarget())
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{Path: path}, errors.Join(stopErr, fmt.Errorf("runner service: remove %s: %w", path, err))
	}
	state := State{Path: path, Detail: "the sparkwing runner service is stopped and removed"}
	if stopErr != nil {
		state.Detail = "the plist is removed but launchd reported an error unloading it"
	}
	return state, stopErr
}

func (h Host) serviceTarget() string { return fmt.Sprintf("gui/%d/%s", h.UID, Label) }

func (h Host) domainTarget() string { return fmt.Sprintf("gui/%d", h.UID) }

func (h Host) run(ctx context.Context, name string, args ...string) (string, error) {
	if h.Exec == nil {
		return "", errors.New("runner service: Host.Exec is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return h.Exec(ctx, name, args...)
}

func requireHost(h Host) error {
	if h.Binary == "" || !filepath.IsAbs(h.Binary) {
		return fmt.Errorf("runner service: Host.Binary must be an absolute path, got %q", h.Binary)
	}
	if h.ConfigPath == "" || !filepath.IsAbs(h.ConfigPath) {
		return fmt.Errorf("runner service: Host.ConfigPath must be an absolute path, got %q", h.ConfigPath)
	}
	return nil
}

func unsupported(goos string) error {
	if goos == "" {
		goos = "unknown"
	}
	return fmt.Errorf("runner service: %s: %w", goos, ErrUnsupported)
}

type managed struct {
	exists bool
	ours   bool
	body   string
}

func (m managed) foreign() bool { return m.exists && !m.ours }

func readManaged(path string) (managed, error) {
	body, exists, ours, err := unitfile.Read(path, Marker)
	return managed{body: body, exists: exists, ours: ours}, err
}

func foreignState(path string) State {
	return State{Path: path, Foreign: true, Detail: fmt.Sprintf("%s exists but sparkwing did not write it, so sparkwing will not touch it", path)}
}

func foreignError(path string) error {
	return fmt.Errorf("runner service: %s was not written by sparkwing (it lacks %q); move it aside before installing", path, Marker)
}

func writeManaged(path, body string) error {
	return unitfile.Write(path, body)
}

// safety: launchd's StandardOutPath creates the log file but not its directory.
func ensureLogDir(logPath string) error {
	if logPath == "" {
		return nil
	}
	dir := filepath.Dir(logPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("runner service: create %s: %w", dir, err)
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

// safety: an empty parse means the file names no config, which no comparison can clear.
func sameConfig(found, want string) bool {
	return found != "" && found == want
}

func mismatchState(path, found, want string) State {
	return State{
		Path: path, Installed: true, Mismatch: true,
		Detail: fmt.Sprintf("%s runs the runner config %s, not %s, so sparkwing left it alone", path, describeConfig(found), want),
	}
}

func describeConfig(found string) string {
	if found == "" {
		return "(none it could read)"
	}
	return found
}

// safety: the unit is the only record of the config it runs; nothing keeps a sidecar.
func unitConfigPath(unit string) string {
	return configArgument(unitArguments(unit))
}

func unitArguments(unit string) []string {
	return unitfile.Arguments("linux", unit)
}

// safety: the plist is the only record of the config it runs; nothing keeps a sidecar.
func plistConfigPath(body []byte) string {
	return configArgument(unitfile.PlistArguments(body))
}

func configArgument(words []string) string {
	if len(words) < 2 || words[1] != "start" {
		return ""
	}
	flags, err := runnerflags.Parse("start", words[2:], io.Discard)
	if err != nil {
		return ""
	}
	config, _ := flags.GetString("config")
	return config
}

func serviceUnit(h Host) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", Marker)
	b.WriteString("[Unit]\n")
	b.WriteString("Description=sparkwing-runner (pulls jobs from a shared sparkwing controller)\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\nType=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s\n", unitfile.Command([]string{h.Binary, "start", "--config", h.ConfigPath}))
	// safety: a runner the operator stopped must stay stopped.
	b.WriteString("Restart=on-failure\nRestartSec=10\n")
	b.WriteString("StandardOutput=journal\nStandardError=journal\n")
	b.WriteString("KillMode=mixed\nKillSignal=SIGTERM\nTimeoutStopSec=30\n\n")
	b.WriteString("[Install]\nWantedBy=default.target\n")
	return b.String()
}

func agentPlist(h Host) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	fmt.Fprintf(&b, "<!-- %s -->\n", Marker)
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "  <key>Label</key>\n  <string>%s</string>\n\n", unitfile.XMLText(Label))
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range []string{h.Binary, "start", "--config", h.ConfigPath} {
		fmt.Fprintf(&b, "    <string>%s</string>\n", unitfile.XMLText(arg))
	}
	b.WriteString("  </array>\n\n")
	b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	fmt.Fprintf(&b, "    <key>HOME</key>\n    <string>%s</string>\n", unitfile.XMLText(h.Home))
	// safety: a launchd job inherits almost nothing, so the jobs' docker and git come from here.
	b.WriteString("    <key>PATH</key>\n    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>\n")
	b.WriteString("  </dict>\n\n")
	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	b.WriteString("  <key>KeepAlive</key>\n  <dict>\n")
	b.WriteString("    <key>SuccessfulExit</key>\n    <false/>\n")
	b.WriteString("    <key>Crashed</key>\n    <true/>\n")
	b.WriteString("  </dict>\n")
	b.WriteString("  <key>ThrottleInterval</key>\n  <integer>10</integer>\n")
	b.WriteString("  <key>ProcessType</key>\n  <string>Background</string>\n\n")
	if h.LogPath != "" {
		fmt.Fprintf(&b, "  <key>StandardOutPath</key>\n  <string>%s</string>\n", unitfile.XMLText(h.LogPath))
		fmt.Fprintf(&b, "  <key>StandardErrorPath</key>\n  <string>%s</string>\n", unitfile.XMLText(h.LogPath))
	}
	fmt.Fprintf(&b, "  <key>WorkingDirectory</key>\n  <string>%s</string>\n", unitfile.XMLText(h.Home))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}
