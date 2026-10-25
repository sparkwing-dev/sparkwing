package crontimer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

func (h Host) unitBase() string {
	if i := h.instance(); i != "" {
		return "sparkwing-crons-" + i
	}
	return "sparkwing-crons"
}

func (h Host) serviceName() string { return h.unitBase() + ".service" }

func (h Host) timerName() string { return h.unitBase() + ".timer" }

// UnitDir reports the systemd user unit directory for a host.
func UnitDir(h Host) string {
	return filepath.Join(h.ConfigHome, "systemd", "user")
}

func linuxPaths(h Host) (service, timer string, err error) {
	if h.ConfigHome == "" {
		return "", "", errors.New("crontimer: Host.ConfigHome is required on linux")
	}
	dir := UnitDir(h)
	return filepath.Join(dir, h.serviceName()), filepath.Join(dir, h.timerName()), nil
}

func installLinux(ctx context.Context, h Host) (State, error) {
	if err := requireBinary(h); err != nil {
		return State{}, err
	}
	servicePath, timerPath, err := linuxPaths(h)
	if err != nil {
		return State{}, err
	}
	service, timer, err := readLinuxFiles(servicePath, timerPath)
	if err != nil {
		return State{}, err
	}
	if foreign := firstForeign(service, timer); foreign != "" {
		return State{Path: timerPath, Foreign: true, Detail: foreignDetail(foreign)},
			fmt.Errorf("crontimer: %s was not written by sparkwing (it lacks %q); move it aside before installing", foreign, Marker)
	}
	if service.exists && timerHome(unitfile.Arguments("linux", service.body)) != h.SparkwingHome {
		return State{}, errors.New(otherHomeDetail(h, servicePath))
	}
	if _, err := refreshPreflight(ctx, h, timerPath); err != nil {
		return State{}, err
	}
	if err := ensureLogDir(h.LogPath); err != nil {
		return State{Path: timerPath}, err
	}
	if err := writeManaged(servicePath, serviceUnit(h)); err != nil {
		return State{Path: timerPath}, err
	}
	if err := writeManaged(timerPath, timerUnit(h)); err != nil {
		return State{Path: timerPath}, err
	}

	state := commandState(h, timerPath, serviceUnit(h))
	if out, err := h.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		state.Detail = "the timer's files are written but systemd did not reload them"
		return state, fmt.Errorf("crontimer: systemctl --user daemon-reload: %w: %s", err, strings.TrimSpace(out))
	}
	if out, err := h.run(ctx, "systemctl", "--user", "enable", "--now", h.timerName()); err != nil {
		state.Detail = "the timer's files are written but systemd did not enable them"
		return state, fmt.Errorf("crontimer: systemctl --user enable --now %s: %w: %s", h.timerName(), err, strings.TrimSpace(out))
	}
	state.Enabled = true
	state.Detail = fmt.Sprintf("%s is enabled and runs %s __tick every minute", h.timerName(), h.Binary)
	return state, nil
}

func uninstallLinux(ctx context.Context, h Host) (State, error) {
	servicePath, timerPath, err := linuxPaths(h)
	if err != nil {
		return State{}, err
	}
	service, timer, err := readLinuxFiles(servicePath, timerPath)
	if err != nil {
		return State{}, err
	}
	if foreign := firstForeign(service, timer); foreign != "" {
		return State{Path: timerPath, Foreign: true, Detail: foreignDetail(foreign)}, nil
	}
	if !service.exists && !timer.exists {
		return State{Path: timerPath, Detail: "no sparkwing cron timer is installed here"}, nil
	}
	if service.exists && timerHome(unitfile.Arguments("linux", service.body)) != h.SparkwingHome {
		return State{}, errors.New(otherHomeDetail(h, servicePath))
	}

	var disableErr error
	loaded := loadedElsewhere(ctx, h, timerPath)
	if loaded == "" {
		if out, err := h.run(ctx, "systemctl", "--user", "disable", "--now", h.timerName()); err != nil && !notLoaded(out) {
			disableErr = fmt.Errorf("crontimer: systemctl --user disable --now %s: %w: %s", h.timerName(), err, strings.TrimSpace(out))
		}
	}
	if err := removeManaged(service, timer); err != nil {
		return State{Path: timerPath}, errors.Join(disableErr, err)
	}
	state := State{Path: timerPath, Detail: "the sparkwing cron timer is removed"}
	if loaded != "" {
		state.Detail = fmt.Sprintf("the files under %s are removed; systemd still runs %s from %s, which is left alone", filepath.Dir(timerPath), h.timerName(), loaded)
	}
	if out, err := h.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return state, errors.Join(disableErr, fmt.Errorf("crontimer: systemctl --user daemon-reload: %w: %s", err, strings.TrimSpace(out)))
	}
	if disableErr != nil {
		state.Detail = "the timer's files are removed but systemd reported an error stopping it"
	}
	return state, disableErr
}

func readLinuxFiles(servicePath, timerPath string) (service, timer managed, err error) {
	if service, err = readManaged(servicePath); err != nil {
		return managed{}, managed{}, err
	}
	if timer, err = readManaged(timerPath); err != nil {
		return managed{}, managed{}, err
	}
	return service, timer, nil
}

func firstForeign(files ...managed) string {
	for _, f := range files {
		if f.exists && !f.ours {
			return f.path
		}
	}
	return ""
}

func foreignDetail(path string) string {
	return fmt.Sprintf("%s exists but sparkwing did not write it, so sparkwing will not touch it", path)
}

func serviceUnit(h Host) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", Marker)
	b.WriteString("[Unit]\nDescription=sparkwing __tick\n\n")
	// safety: a oneshot's cgroup is killed when its main process exits, which would take
	// the run consumer and the admission daemon the tick just started with it.
	b.WriteString("[Service]\nType=oneshot\nKillMode=process\n")
	// safety: a tick that hangs on a lock or a store would otherwise hold the
	// unit active and swallow every following minute.
	b.WriteString("TimeoutStartSec=5m\n")
	fmt.Fprintf(&b, "%s\nExecStart=%s\n", unitfile.VersionRecord("linux", h.Version), systemdCommand(h))
	for _, kv := range envPairs(h) {
		fmt.Fprintf(&b, "Environment=%s\n", unitfile.Assignment(kv[0], kv[1]))
	}
	if h.LogPath != "" {
		fmt.Fprintf(&b, "StandardOutput=append:%s\n", h.LogPath)
		fmt.Fprintf(&b, "StandardError=append:%s\n", h.LogPath)
	}
	return b.String()
}

func timerUnit(h Host) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", Marker)
	b.WriteString("[Unit]\nDescription=sparkwing __tick every minute\n\n")
	b.WriteString("[Timer]\n")
	b.WriteString("OnCalendar=*-*-* *:*:00\n")
	b.WriteString("AccuracySec=1s\n")
	b.WriteString("Persistent=true\n")
	fmt.Fprintf(&b, "Unit=%s\n\n", h.serviceName())
	b.WriteString("[Install]\nWantedBy=timers.target\n")
	return b.String()
}

// safety: systemctl addresses units by name, so a ConfigHome that is not the session's would
// otherwise be asked about, enabled, and disabled against a stranger's file.
func loadedElsewhere(ctx context.Context, h Host, timerPath string) string {
	out, err := h.run(ctx, "systemctl", "--user", "show", "-p", "FragmentPath", "--value", h.timerName())
	if err != nil {
		return ""
	}
	loaded := strings.TrimSpace(out)
	if loaded == "" || loaded == timerPath {
		return ""
	}
	return loaded
}
