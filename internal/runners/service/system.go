package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

// SystemHost places a runner in the system service manager under a dedicated account.
type SystemHost struct {
	GOOS    string
	Account Account
	// UnitDir is /etc/systemd/system or /Library/LaunchDaemons; a suite substitutes a temporary directory.
	UnitDir    string
	Binary     string
	ConfigPath string
	StateHome  string
	LogPath    string
	Path       string
	Exec       func(context.Context, string, ...string) (string, error)
}

// SystemUnitDir reports where the native manager reads system services.
func SystemUnitDir(goos string) string {
	if goos == "darwin" {
		return "/Library/LaunchDaemons"
	}
	return "/etc/systemd/system"
}

// SystemUnitName names the systemd unit for an account's runner.
func SystemUnitName(account string) string { return "sparkwing-runner-" + account + ".service" }

// SystemLabel names the launchd daemon for an account's runner.
func SystemLabel(account string) string { return label + "." + account }

// SystemPath reports the registration file for a host.
func SystemPath(h SystemHost) (string, error) {
	if !filepath.IsAbs(h.UnitDir) {
		return "", errors.New("runner service: SystemHost.UnitDir must be an absolute path")
	}
	if err := ValidAccountName(h.Account.Name); err != nil {
		return "", err
	}
	switch h.GOOS {
	case "linux":
		return filepath.Join(h.UnitDir, SystemUnitName(h.Account.Name)), nil
	case "darwin":
		return filepath.Join(h.UnitDir, SystemLabel(h.Account.Name)+".plist"), nil
	default:
		return "", unsupported(h.GOOS)
	}
}

// InstallSystem writes the registration and starts or restarts the service.
func InstallSystem(ctx context.Context, h SystemHost) (State, error) {
	path, err := SystemPath(h)
	if err != nil {
		return State{}, err
	}
	for name, value := range map[string]string{"Binary": h.Binary, "ConfigPath": h.ConfigPath, "StateHome": h.StateHome} {
		if !filepath.IsAbs(value) {
			return State{}, fmt.Errorf("runner service: SystemHost.%s must be an absolute path, got %q", name, value)
		}
	}
	_, exists, managed, err := unitfile.Read(path, marker)
	if err != nil {
		return State{}, err
	}
	if exists && !managed {
		return foreignState(path), foreignError(path)
	}
	loaded, err := h.registration(ctx, path)
	if err != nil {
		return State{}, err
	}
	body := systemUnit(h)
	if h.GOOS == "darwin" {
		body = daemonPlist(h)
	}
	if err := unitfile.Write(path, body); err != nil {
		return State{}, err
	}
	state := State{Path: path, Installed: true, Binary: h.Binary, Arguments: []string{"start", "--config", h.ConfigPath}}
	if h.GOOS == "linux" {
		unit := SystemUnitName(h.Account.Name)
		err = runCommand(ctx, h.hostView(), "systemctl", "daemon-reload")
		if err == nil {
			err = runCommand(ctx, h.hostView(), "systemctl", "enable", unit)
		}
		if err == nil {
			// safety: restart, not start, so a reinstall picks up a changed credential or binary.
			err = runCommand(ctx, h.hostView(), "systemctl", "restart", unit)
		}
	} else {
		if loaded {
			err = runCommand(ctx, h.hostView(), "launchctl", "bootout", h.target())
		}
		if err == nil {
			err = runCommand(ctx, h.hostView(), "launchctl", "bootstrap", "system", path)
		}
	}
	state.Running = err == nil
	state.Detail = fmt.Sprintf("the runner service runs as %s", h.Account.Name)
	if err != nil {
		state.Detail = "the runner registration is installed but the service manager failed to start it"
	}
	return state, err
}

// UninstallSystem stops the account's service and removes its registration.
func UninstallSystem(ctx context.Context, h SystemHost) (State, error) {
	path, err := SystemPath(h)
	if err != nil {
		return State{}, err
	}
	_, exists, managed, err := unitfile.Read(path, marker)
	if err != nil {
		return State{}, err
	}
	if exists && !managed {
		return foreignState(path), foreignError(path)
	}
	state := State{Path: path, Detail: "no sparkwing runner service is installed for " + h.Account.Name}
	if !exists {
		return state, nil
	}
	loaded, err := h.registration(ctx, path)
	if err != nil {
		return state, err
	}
	var stopErr error
	if h.GOOS == "linux" {
		unit := SystemUnitName(h.Account.Name)
		if out, err := h.run(ctx, "systemctl", "disable", "--now", unit); err != nil && !notLoaded(out) {
			stopErr = fmt.Errorf("runner service: disable %s: %w: %s", unit, err, strings.TrimSpace(out))
		}
	} else if loaded {
		stopErr = runCommand(ctx, h.hostView(), "launchctl", "bootout", h.target())
	}
	if stopErr != nil {
		return state, stopErr
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return state, fmt.Errorf("runner service: remove %s: %w", path, err)
	}
	state.Detail = "the sparkwing runner service for " + h.Account.Name + " is stopped and removed"
	if h.GOOS == "linux" {
		return state, runCommand(ctx, h.hostView(), "systemctl", "daemon-reload")
	}
	return state, nil
}

func (h SystemHost) target() string { return "system/" + SystemLabel(h.Account.Name) }

func (h SystemHost) hostView() Host { return Host{GOOS: h.GOOS, Exec: h.Exec} }

func (h SystemHost) run(ctx context.Context, name string, args ...string) (string, error) {
	return h.hostView().run(ctx, name, args...)
}

// safety: a same-named service loaded from another file belongs to someone else.
func (h SystemHost) registration(ctx context.Context, path string) (bool, error) {
	if h.GOOS == "linux" {
		unit := SystemUnitName(h.Account.Name)
		out, err := h.run(ctx, "systemctl", "show", "-p", "FragmentPath", "--value", unit)
		if err != nil {
			return false, fmt.Errorf("runner service: inspect %s registration: %w: %s", unit, err, strings.TrimSpace(out))
		}
		if loaded := strings.TrimSpace(out); loaded != "" && !unitfile.SameSource(loaded, path) {
			return false, fmt.Errorf("runner service: %s is loaded from %s, not %s", unit, loaded, path)
		}
		return strings.TrimSpace(out) != "", nil
	}
	out, err := h.run(ctx, "launchctl", "print", h.target())
	if err != nil && ctx.Err() == nil && notLoaded(out) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("runner service: inspect launchd registration: %w: %s", err, strings.TrimSpace(out))
	}
	return true, unitfile.CheckAgentSource(out, path)
}

func systemUnit(h SystemHost) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", marker)
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=sparkwing-runner as %s (pulls jobs from a shared sparkwing controller)\n", h.Account.Name)
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\nType=simple\n")
	fmt.Fprintf(&b, "User=%s\nGroup=%s\n", h.Account.Name, h.Account.Name)
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", unitfile.SystemdWord(h.StateHome))
	fmt.Fprintf(&b, "ExecStart=%s\n", unitfile.Command([]string{h.Binary, "start", "--config", h.ConfigPath}))
	fmt.Fprintf(&b, "Environment=%s\n", unitfile.Assignment("HOME", h.Account.Home))
	fmt.Fprintf(&b, "Environment=%s\n", unitfile.Assignment("SPARKWING_HOME", h.StateHome))
	if h.Path != "" {
		fmt.Fprintf(&b, "Environment=%s\n", unitfile.Assignment("PATH", h.Path))
	}
	b.WriteString("UMask=0077\n")
	// safety: the account's uid keeps jobs out of 0600 and 0700 files; these
	// also hide every home directory, including world-readable ones, and keep
	// jobs from gaining privileges through setuid binaries.
	b.WriteString("ProtectHome=yes\nNoNewPrivileges=yes\nPrivateTmp=yes\nProtectSystem=full\n")
	b.WriteString("Restart=on-failure\nRestartSec=10\n")
	b.WriteString("StandardOutput=journal\nStandardError=journal\n")
	b.WriteString("KillMode=mixed\nKillSignal=SIGTERM\nTimeoutStopSec=30\n\n")
	b.WriteString("[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

func daemonPlist(h SystemHost) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	fmt.Fprintf(&b, "<!-- %s -->\n", marker)
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	fmt.Fprintf(&b, "  <key>Label</key>\n  <string>%s</string>\n\n", unitfile.XMLText(SystemLabel(h.Account.Name)))
	fmt.Fprintf(&b, "  <key>UserName</key>\n  <string>%s</string>\n", unitfile.XMLText(h.Account.Name))
	fmt.Fprintf(&b, "  <key>GroupName</key>\n  <string>%s</string>\n\n", unitfile.XMLText(h.Account.Name))
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range []string{h.Binary, "start", "--config", h.ConfigPath} {
		fmt.Fprintf(&b, "    <string>%s</string>\n", unitfile.XMLText(arg))
	}
	b.WriteString("  </array>\n\n")
	b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	fmt.Fprintf(&b, "    <key>HOME</key>\n    <string>%s</string>\n", unitfile.XMLText(h.Account.Home))
	fmt.Fprintf(&b, "    <key>SPARKWING_HOME</key>\n    <string>%s</string>\n", unitfile.XMLText(h.StateHome))
	path := h.Path
	if path == "" {
		path = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	}
	fmt.Fprintf(&b, "    <key>PATH</key>\n    <string>%s</string>\n", unitfile.XMLText(path))
	b.WriteString("  </dict>\n\n")
	b.WriteString("  <key>Umask</key>\n  <integer>63</integer>\n")
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
	fmt.Fprintf(&b, "  <key>WorkingDirectory</key>\n  <string>%s</string>\n", unitfile.XMLText(h.StateHome))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// ServicePath keeps PATH entries the account can reach: entries under people's
// homes are dropped and reported, and the account's own bin directory leads.
func ServicePath(path, accountBin string) (kept string, dropped []string) {
	entries := []string{accountBin}
	for _, entry := range filepath.SplitList(path) {
		clean := filepath.Clean(entry)
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		if underAny(clean, peopleRoots) {
			dropped = append(dropped, entry)
			continue
		}
		if clean != accountBin {
			entries = append(entries, clean)
		}
	}
	return strings.Join(entries, string(os.PathListSeparator)), dropped
}
