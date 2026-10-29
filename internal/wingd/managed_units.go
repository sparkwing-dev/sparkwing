package wingd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

// NewManagedUnitManager manages the native controller and schedule units for one captured state home.
// Host directories and environment are installation inputs; no store is opened. Windows has no native units.
func NewManagedUnitManager(host crontimer.Host) (ManagedUnitManager, error) {
	if host.GOOS != "linux" && host.GOOS != "darwin" && host.GOOS != "windows" {
		return nil, fmt.Errorf("wingd: unsupported managed-unit platform %q", host.GOOS)
	}
	if !filepath.IsAbs(host.Home) || !filepath.IsAbs(host.SparkwingHome) || (host.GOOS == "linux" && !filepath.IsAbs(host.ConfigHome)) {
		return nil, errors.New("wingd: managed unit homes must be absolute")
	}
	host.Home, host.SparkwingHome = filepath.Clean(host.Home), filepath.Clean(host.SparkwingHome)
	host.Env = maps.Clone(host.Env)
	logPath, err := LogPath(host.SparkwingHome)
	if err != nil {
		return nil, err
	}
	return &nativeUnits{host: host, logPath: logPath, gate: make(chan struct{}, 1)}, nil
}

type nativeUnits struct {
	host    crontimer.Host
	logPath string
	gate    chan struct{}
}

func (m *nativeUnits) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *nativeUnits) name() string {
	name, separator := "sparkwing-controller", "-"
	if m.host.GOOS == "darwin" {
		name, separator = "dev.sparkwing.controller", "."
	}
	if instance := unitfile.Instance(m.host.Home, m.host.SparkwingHome); instance != "" {
		name += separator + instance
	}
	return name
}

func (m *nativeUnits) path() string {
	if m.host.GOOS == "darwin" {
		return filepath.Join(crontimer.AgentDir(m.host), m.name()+".plist")
	}
	return filepath.Join(crontimer.UnitDir(m.host), m.name()+".service")
}

func (m *nativeUnits) target() string { return fmt.Sprintf("gui/%d/%s", m.host.UID, m.name()) }
func (m *nativeUnits) domain() string { return fmt.Sprintf("gui/%d", m.host.UID) }

func (m *nativeUnits) controller() (ManagedUnit, string, bool, error) {
	body, exists, managed, err := unitfile.Read(m.path(), crontimer.Marker)
	if err != nil || !exists {
		return ManagedUnit{}, body, exists, err
	}
	if !managed {
		return ManagedUnit{}, body, exists, fmt.Errorf("wingd: native unit %s was not written by Sparkwing", m.path())
	}
	args := unitfile.Arguments(m.host.GOOS, body)
	unit := ManagedUnit{Kind: ManagedController, Name: m.path(), Version: unitfile.Version(body)}
	if len(args) > 0 {
		unit.Executable, unit.Arguments = args[0], slices.Clone(args[1:])
	}
	if len(args) == 6 && slices.Equal(args[1:5], []string{"wingd", "run", "--stay", "--home"}) {
		unit.Home = args[5]
	}
	return unit, body, exists, nil
}

func (m *nativeUnits) inspect(ctx context.Context) ([]ManagedUnit, error) {
	if m.host.GOOS == "windows" {
		return nil, nil
	}
	controller, _, exists, err := m.controller()
	if err != nil {
		return nil, err
	}
	var units []ManagedUnit
	if exists {
		units = append(units, controller)
	}
	timer, err := crontimer.Inspect(ctx, m.host)
	if err != nil {
		return nil, err
	}
	if timer.Foreign {
		return nil, errors.New(timer.Detail)
	}
	if timer.Installed {
		units = append(units, ManagedUnit{
			Kind: ManagedSchedule, Name: timer.Path, Executable: timer.Binary,
			Version: timer.Version, Arguments: timer.Arguments, Home: timer.Home,
		})
	}
	return units, nil
}

func (m *nativeUnits) Inspect(ctx context.Context) ([]ManagedUnit, error) {
	if err := m.lock(ctx); err != nil {
		return nil, err
	}
	defer func() { <-m.gate }()
	return m.inspect(ctx)
}

func (m *nativeUnits) validate(ctx context.Context, executable, version string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.host.GOOS == "windows" {
		return errors.New("wingd: Windows supports dashboard start --stay and daemon schedules; native service installation is unavailable")
	}
	if !filepath.IsAbs(executable) || strings.ContainsAny(executable, "\r\n\x00") || version == "" || strings.ContainsAny(version, "\r\n<>\x00") {
		return errors.New("wingd: an absolute executable and a one-line release version are required")
	}
	if m.host.Exec == nil {
		return errors.New("wingd: native service command executor is required")
	}
	return nil
}

func (m *nativeUnits) InstallController(ctx context.Context, opts LocalStartOptions) error {
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer func() { <-m.gate }()
	if err := m.validate(ctx, opts.Executable, opts.Version); err != nil {
		return err
	}
	if !filepath.IsAbs(opts.Home) || filepath.Clean(opts.Home) != m.host.SparkwingHome || opts.Standalone || opts.DashboardAddress != "" {
		return errors.New("wingd: installation requires the captured home; start the dashboard through its lifecycle client")
	}
	unit, _, exists, err := m.controller()
	if err != nil {
		return err
	}
	if exists && unit.Home != m.host.SparkwingHome {
		return errors.New("wingd: installed controller belongs to another home or uses unsupported arguments")
	}
	loaded, err := m.loaded(ctx)
	if err != nil {
		return err
	}
	if err := fssecure.EnsureDir(filepath.Dir(m.logPath)); err != nil {
		return err
	}
	body := m.render(opts.Executable, opts.Version)
	if err := unitfile.Write(m.path(), body); err != nil {
		return err
	}
	if m.host.GOOS == "linux" {
		if err := m.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		return m.run(ctx, "systemctl", "--user", "enable", "--now", m.name()+".service")
	}
	return m.reloadAgent(ctx, loaded)
}

func (m *nativeUnits) Refresh(ctx context.Context, executable, version string) error {
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer func() { <-m.gate }()
	if m.host.GOOS == "windows" {
		return nil
	}
	if err := m.validate(ctx, executable, version); err != nil {
		return err
	}
	units, err := m.inspect(ctx)
	if err != nil {
		return err
	}
	for _, unit := range units {
		if unit.Home != m.host.SparkwingHome {
			return fmt.Errorf("wingd: %s belongs to another home or uses unsupported arguments", unit.Name)
		}
	}
	unit, body, exists, err := m.controller()
	if err != nil {
		return err
	}
	loaded := false
	if exists {
		loaded, err = m.loaded(ctx)
		if err != nil {
			return err
		}
	}
	host := m.host
	host.Binary, host.Version = executable, version
	if _, err := crontimer.Refresh(ctx, host); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	body, err = unitfile.RewriteCommand(m.host.GOOS, body, append([]string{executable}, unit.Arguments...), version)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unitfile.Write(m.path(), body); err != nil {
		return err
	}
	if m.host.GOOS == "linux" {
		if err := m.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		return m.run(ctx, "systemctl", "--user", "restart", m.name()+".service")
	}
	return m.reloadAgent(ctx, loaded)
}

func (m *nativeUnits) UninstallController(ctx context.Context) error {
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer func() { <-m.gate }()
	if m.host.GOOS == "windows" {
		return errors.New("wingd: Windows supports --stay; there is no installed controller service")
	}
	unit, _, exists, err := m.controller()
	if err != nil || !exists {
		return err
	}
	if unit.Home != m.host.SparkwingHome {
		return errors.New("wingd: refusing to uninstall another home's controller")
	}
	loaded, err := m.loaded(ctx)
	if err != nil {
		return err
	}
	if m.host.GOOS == "linux" {
		err = m.run(ctx, "systemctl", "--user", "disable", "--now", m.name()+".service")
	} else if loaded {
		err = m.run(ctx, "launchctl", "bootout", m.target())
	}
	if err != nil {
		return err
	}
	if err := os.Remove(m.path()); err != nil {
		return err
	}
	if m.host.GOOS == "linux" {
		return m.run(ctx, "systemctl", "--user", "daemon-reload")
	}
	return nil
}

func (m *nativeUnits) run(ctx context.Context, command string, args ...string) error {
	_, err := m.output(ctx, command, args...)
	return err
}

func (m *nativeUnits) output(ctx context.Context, command string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if m.host.Exec == nil {
		return "", errors.New("wingd: native service command executor is required")
	}
	out, err := m.host.Exec(ctx, command, args...)
	if err != nil {
		return out, fmt.Errorf("wingd: %s %s: %w: %s", command, strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return out, nil
}

func (m *nativeUnits) loaded(ctx context.Context) (bool, error) {
	if m.host.GOOS == "linux" {
		return true, unitfile.CheckSystemdSource(ctx, m.host.Exec, m.name()+".service", m.path())
	}
	out, err := m.output(ctx, "launchctl", "print", m.target())
	if err != nil && ctx.Err() == nil && strings.Contains(strings.ToLower(out), "could not find service") {
		return false, nil
	}
	if err == nil {
		err = unitfile.CheckAgentSource(out, m.path())
	}
	return err == nil, err
}

func (m *nativeUnits) reloadAgent(ctx context.Context, loaded bool) error {
	if loaded {
		if err := m.run(ctx, "launchctl", "bootout", m.target()); err != nil {
			return err
		}
	}
	return m.run(ctx, "launchctl", "bootstrap", m.domain(), m.path())
}

func (m *nativeUnits) render(executable, version string) string {
	args := []string{executable, "wingd", "run", "--stay", "--home", m.host.SparkwingHome}
	if m.host.GOOS == "linux" {
		return fmt.Sprintf("# %s\n%s\n[Unit]\nDescription=Sparkwing local controller\n[Service]\nType=simple\nExecStart=%s\nEnvironment=%s\nRestart=on-failure\n[Install]\nWantedBy=default.target\n", crontimer.Marker, unitfile.VersionRecord("linux", version), unitfile.Command(args), unitfile.Assignment("PATH", m.host.PathEnv))
	}
	var body strings.Builder
	fmt.Fprintf(&body, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!-- %s -->\n%s\n<plist version=\"1.0\"><dict>\n<key>Label</key><string>%s</string>\n<key>ProgramArguments</key><array>\n", crontimer.Marker, unitfile.VersionRecord("darwin", version), unitfile.XMLText(m.name()))
	for _, arg := range args {
		fmt.Fprintf(&body, "<string>%s</string>\n", unitfile.XMLText(arg))
	}
	fmt.Fprintf(&body, "</array>\n<key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string></dict>\n", unitfile.XMLText(m.host.PathEnv))
	fmt.Fprintf(&body, "<key>StandardOutPath</key><string>%s</string>\n<key>StandardErrorPath</key><string>%s</string>\n", unitfile.XMLText(m.logPath), unitfile.XMLText(m.logPath))
	body.WriteString("<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n</dict></plist>\n")
	return body.String()
}
