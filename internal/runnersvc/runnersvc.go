// Package runnersvc installs the per-user OS service that runs
// `sparkwing-runner agent`: a systemd user unit on Linux, a launchd agent on
// macOS. It writes the same unit and plist, under the same names, that
// install/service-install.sh writes from its templates, so a machine enrolled
// by either route has one service.
//
// A file this package writes carries [Marker]; a file the shell installer
// wrote carries its own. A file with neither is left alone, so a unit an
// operator wrote by hand is never overwritten or removed. All contact with the
// machine -- systemctl, launchctl, the home and config directories, the binary
// path -- arrives through [Host], so a test decides what the machine looks
// like and both platforms are exercised anywhere.
package runnersvc

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// Marker appears verbatim in every file this package writes.
	Marker = "Installed by sparkwing"

	// safety: the shell installer's own marker, so a machine it enrolled is managed from here rather than refused.
	installerMarker = "Installed by install/service-install.sh"

	// ServiceName is the systemd user unit that runs the agent.
	ServiceName = "sparkwing-runner.service"

	// Label is the launchd label of the agent.
	Label = "com.sparkwing.runner"

	// PlistName is the file launchd reads the agent from.
	PlistName = Label + ".plist"
)

// hack: 0600, matching the shell installer -- the plist names the user's home.
const fileMode os.FileMode = 0o600

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

	// ConfigPath is the agent.yaml the service reads its credential from.
	ConfigPath string

	// LogPath receives the agent's stdout and stderr on macOS. systemd
	// journals the unit instead.
	LogPath string

	// UID names the launchd domain gui/<uid>.
	UID int

	// Exec runs systemctl or launchctl and returns stdout and stderr combined.
	Exec func(name string, args ...string) (string, error)
}

// State describes the service as it exists on the host.
type State struct {
	// Installed reports a managed unit or plist at the expected path.
	Installed bool `json:"installed"`

	// Foreign reports a file at that path that neither this package nor the
	// shell installer wrote. Such a file is never overwritten or removed.
	Foreign bool `json:"foreign"`

	// Running reports that the service manager accepted the start or stop.
	Running bool `json:"running"`

	// Path is the unit or plist path.
	Path string `json:"path"`

	// Detail is one sentence for a human.
	Detail string `json:"detail,omitempty"`
}

// Install writes the service file, starts it, and reports the state that
// follows. A foreign file is an error and nothing is written.
func Install(h Host) (State, error) {
	switch h.GOOS {
	case "linux":
		return installLinux(h)
	case "darwin":
		return installDarwin(h)
	}
	return State{}, unsupported(h.GOOS)
}

// Uninstall stops the service and removes the file sparkwing wrote. A foreign
// file is left in place and reported through State.Foreign.
func Uninstall(h Host) (State, error) {
	switch h.GOOS {
	case "linux":
		return uninstallLinux(h)
	case "darwin":
		return uninstallDarwin(h)
	}
	return State{}, unsupported(h.GOOS)
}

// DefaultExec runs a command and returns its output, stdout and stderr
// combined, for a Host that talks to the real machine.
func DefaultExec(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
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

func installLinux(h Host) (State, error) {
	if err := requireHost(h); err != nil {
		return State{}, err
	}
	if h.ConfigHome == "" {
		return State{}, errors.New("runnersvc: Host.ConfigHome is required on linux")
	}
	path := UnitPath(h)
	existing, err := readManaged(path)
	if err != nil {
		return State{}, err
	}
	if existing.foreign() {
		return foreignState(path), foreignError(path)
	}
	if err := writeManaged(path, serviceUnit(h)); err != nil {
		return State{Path: path}, err
	}

	state := State{Installed: true, Path: path}
	if out, err := h.run("systemctl", "--user", "daemon-reload"); err != nil {
		state.Detail = "the unit is written but systemd did not reload it"
		return state, fmt.Errorf("runnersvc: systemctl --user daemon-reload: %w: %s", err, strings.TrimSpace(out))
	}
	if out, err := h.run("systemctl", "--user", "enable", "--now", ServiceName); err != nil {
		state.Detail = "the unit is written but systemd did not start it"
		return state, fmt.Errorf("runnersvc: systemctl --user enable --now %s: %w: %s", ServiceName, err, strings.TrimSpace(out))
	}
	state.Running = true
	state.Detail = fmt.Sprintf("%s is enabled and running %s agent", ServiceName, h.Binary)
	return state, nil
}

func uninstallLinux(h Host) (State, error) {
	if h.ConfigHome == "" {
		return State{}, errors.New("runnersvc: Host.ConfigHome is required on linux")
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

	var stopErr error
	if out, err := h.run("systemctl", "--user", "disable", "--now", ServiceName); err != nil && !notLoaded(out) {
		stopErr = fmt.Errorf("runnersvc: systemctl --user disable --now %s: %w: %s", ServiceName, err, strings.TrimSpace(out))
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{Path: path}, errors.Join(stopErr, fmt.Errorf("runnersvc: remove %s: %w", path, err))
	}
	state := State{Path: path, Detail: "the sparkwing runner service is stopped and removed"}
	if out, err := h.run("systemctl", "--user", "daemon-reload"); err != nil {
		return state, errors.Join(stopErr, fmt.Errorf("runnersvc: systemctl --user daemon-reload: %w: %s", err, strings.TrimSpace(out)))
	}
	if stopErr != nil {
		state.Detail = "the unit is removed but systemd reported an error stopping it"
	}
	return state, stopErr
}

func installDarwin(h Host) (State, error) {
	if err := requireHost(h); err != nil {
		return State{}, err
	}
	if h.Home == "" {
		return State{}, errors.New("runnersvc: Host.Home is required on darwin")
	}
	path := PlistPath(h)
	existing, err := readManaged(path)
	if err != nil {
		return State{}, err
	}
	if existing.foreign() {
		return foreignState(path), foreignError(path)
	}
	if err := ensureLogDir(h.LogPath); err != nil {
		return State{Path: path}, err
	}
	if err := writeManaged(path, agentPlist(h)); err != nil {
		return State{Path: path}, err
	}

	state := State{Installed: true, Path: path}
	// safety: launchd refuses to bootstrap a label already in the domain, and a
	// first install has nothing loaded, so this failure is ordinary.
	_, _ = h.run("launchctl", "bootout", h.serviceTarget())
	if out, err := h.run("launchctl", "bootstrap", h.domainTarget(), path); err != nil {
		state.Detail = "the plist is written but launchd did not load it"
		return state, fmt.Errorf("runnersvc: launchctl bootstrap %s %s: %w: %s",
			h.domainTarget(), path, err, strings.TrimSpace(out))
	}
	state.Running = true
	state.Detail = fmt.Sprintf("%s is loaded and running %s agent", Label, h.Binary)
	return state, nil
}

func uninstallDarwin(h Host) (State, error) {
	if h.Home == "" {
		return State{}, errors.New("runnersvc: Host.Home is required on darwin")
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

	var stopErr error
	if out, err := h.run("launchctl", "bootout", h.serviceTarget()); err != nil && !notLoaded(out) {
		stopErr = fmt.Errorf("runnersvc: launchctl bootout %s: %w: %s", h.serviceTarget(), err, strings.TrimSpace(out))
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{Path: path}, errors.Join(stopErr, fmt.Errorf("runnersvc: remove %s: %w", path, err))
	}
	state := State{Path: path, Detail: "the sparkwing runner service is stopped and removed"}
	if stopErr != nil {
		state.Detail = "the plist is removed but launchd reported an error unloading it"
	}
	return state, stopErr
}

func (h Host) serviceTarget() string { return fmt.Sprintf("gui/%d/%s", h.UID, Label) }

func (h Host) domainTarget() string { return fmt.Sprintf("gui/%d", h.UID) }

func (h Host) run(name string, args ...string) (string, error) {
	if h.Exec == nil {
		return "", errors.New("runnersvc: Host.Exec is required")
	}
	return h.Exec(name, args...)
}

func requireHost(h Host) error {
	if h.Binary == "" || !filepath.IsAbs(h.Binary) {
		return fmt.Errorf("runnersvc: Host.Binary must be an absolute path, got %q", h.Binary)
	}
	if h.ConfigPath == "" || !filepath.IsAbs(h.ConfigPath) {
		return fmt.Errorf("runnersvc: Host.ConfigPath must be an absolute path, got %q", h.ConfigPath)
	}
	return nil
}

func unsupported(goos string) error {
	if goos == "" {
		goos = "unknown"
	}
	return fmt.Errorf("runnersvc: %s: %w", goos, ErrUnsupported)
}

type managed struct {
	exists bool
	ours   bool
}

func (m managed) foreign() bool { return m.exists && !m.ours }

func readManaged(path string) (managed, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return managed{}, nil
	}
	if err != nil {
		return managed{}, fmt.Errorf("runnersvc: read %s: %w", path, err)
	}
	text := string(body)
	return managed{exists: true, ours: strings.Contains(text, Marker) || strings.Contains(text, installerMarker)}, nil
}

func foreignState(path string) State {
	return State{Path: path, Foreign: true, Detail: fmt.Sprintf("%s exists but sparkwing did not write it, so sparkwing will not touch it", path)}
}

func foreignError(path string) error {
	return fmt.Errorf("runnersvc: %s was not written by sparkwing (it lacks %q); move it aside before installing", path, Marker)
}

func writeManaged(path, body string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("runnersvc: create %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".runnersvc-*.tmp")
	if err != nil {
		return fmt.Errorf("runnersvc: create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() {
		if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "sparkwing: could not clear the temporary file %s: %v\n", tmp, err)
		}
	}()
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("runnersvc: write %s: %w", tmp, err)
	}
	if err := f.Chmod(fileMode); err != nil {
		_ = f.Close()
		return fmt.Errorf("runnersvc: chmod %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("runnersvc: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("runnersvc: rename %s to %s: %w", tmp, path, err)
	}
	return nil
}

// safety: launchd's StandardOutPath creates the log file but not its directory.
func ensureLogDir(logPath string) error {
	if logPath == "" {
		return nil
	}
	dir := filepath.Dir(logPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("runnersvc: create %s: %w", dir, err)
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

func serviceUnit(h Host) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", Marker)
	b.WriteString("[Unit]\n")
	b.WriteString("Description=sparkwing-runner (pulls jobs from a shared sparkwing controller)\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\nType=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s agent --config %s\n", systemdWord(h.Binary), systemdWord(h.ConfigPath))
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
	fmt.Fprintf(&b, "  <key>Label</key>\n  <string>%s</string>\n\n", xmlText(Label))
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range []string{h.Binary, "agent", "--config", h.ConfigPath} {
		fmt.Fprintf(&b, "    <string>%s</string>\n", xmlText(arg))
	}
	b.WriteString("  </array>\n\n")
	b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	fmt.Fprintf(&b, "    <key>HOME</key>\n    <string>%s</string>\n", xmlText(h.Home))
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
		fmt.Fprintf(&b, "  <key>StandardOutPath</key>\n  <string>%s</string>\n", xmlText(h.LogPath))
		fmt.Fprintf(&b, "  <key>StandardErrorPath</key>\n  <string>%s</string>\n", xmlText(h.LogPath))
	}
	fmt.Fprintf(&b, "  <key>WorkingDirectory</key>\n  <string>%s</string>\n", xmlText(h.Home))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// safety: systemd splits an argument on whitespace and reads % as a specifier, so quote one and double the other.
func systemdWord(v string) string {
	if v == "" {
		return `""`
	}
	escaped := strings.ReplaceAll(v, "%", "%%")
	if !strings.ContainsAny(escaped, " \t\n\"'\\") {
		return escaped
	}
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(escaped) + `"`
}

func xmlText(s string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return ""
	}
	return buf.String()
}
