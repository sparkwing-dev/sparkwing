package crontimer

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	// ServiceName is the systemd user unit that runs one tick.
	ServiceName = "sparkwing-crons.service"

	// TimerName is the systemd user timer that starts [ServiceName] each minute.
	TimerName = "sparkwing-crons.timer"
)

// UnitDir reports the systemd user unit directory for a host.
func UnitDir(h Host) string {
	return filepath.Join(h.ConfigHome, "systemd", "user")
}

func linuxPaths(h Host) (service, timer string, err error) {
	if h.ConfigHome == "" {
		return "", "", errors.New("crontimer: Host.ConfigHome is required on linux")
	}
	dir := UnitDir(h)
	return filepath.Join(dir, ServiceName), filepath.Join(dir, TimerName), nil
}

func installLinux(h Host) (State, error) {
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
	if err := ensureLogDir(h.LogPath); err != nil {
		return State{Path: timerPath}, err
	}
	if err := writeManaged(servicePath, serviceUnit(h)); err != nil {
		return State{Path: timerPath}, err
	}
	if err := writeManaged(timerPath, timerUnit(h)); err != nil {
		return State{Path: timerPath}, err
	}

	state := State{Installed: true, Path: timerPath, Binary: h.Binary}
	if out, err := h.run("systemctl", "--user", "daemon-reload"); err != nil {
		state.Detail = "the timer's files are written but systemd did not reload them"
		return state, fmt.Errorf("crontimer: systemctl --user daemon-reload: %w: %s", err, strings.TrimSpace(out))
	}
	if out, err := h.run("systemctl", "--user", "enable", "--now", TimerName); err != nil {
		state.Detail = "the timer's files are written but systemd did not enable them"
		return state, fmt.Errorf("crontimer: systemctl --user enable --now %s: %w: %s", TimerName, err, strings.TrimSpace(out))
	}
	state.Enabled = true
	state.Detail = fmt.Sprintf("%s is enabled and runs %s crons tick every minute", TimerName, h.Binary)
	return state, nil
}

func statusLinux(h Host) (State, error) {
	servicePath, timerPath, err := linuxPaths(h)
	if err != nil {
		return State{}, err
	}
	service, timer, err := readLinuxFiles(servicePath, timerPath)
	if err != nil {
		return State{}, err
	}
	state := linuxFileState(h, service, timer)
	if !state.Installed {
		return state, nil
	}
	enabledOut, enabledErr := h.run("systemctl", "--user", "is-enabled", TimerName)
	activeOut, activeErr := h.run("systemctl", "--user", "is-active", TimerName)
	switch {
	case enabledErr == nil && activeErr == nil:
		state.Enabled = true
		state.Detail = fmt.Sprintf("%s is enabled and active, running %s crons tick every minute", TimerName, state.Binary)
	default:
		state.Detail = fmt.Sprintf("%s is installed but systemd does not report it running: %s", TimerName,
			joinOutput(enabledOut, activeOut))
	}
	if state.Stale {
		state.Detail += fmt.Sprintf("; it runs %s, not %s", state.Binary, h.Binary)
	}
	return state, nil
}

func uninstallLinux(h Host) (State, error) {
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

	var disableErr error
	if out, err := h.run("systemctl", "--user", "disable", "--now", TimerName); err != nil && !notLoaded(out) {
		disableErr = fmt.Errorf("crontimer: systemctl --user disable --now %s: %w: %s", TimerName, err, strings.TrimSpace(out))
	}
	if err := removeManaged(service, timer); err != nil {
		return State{Path: timerPath}, errors.Join(disableErr, err)
	}
	state := State{Path: timerPath, Detail: "the sparkwing cron timer is removed"}
	if out, err := h.run("systemctl", "--user", "daemon-reload"); err != nil {
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

func linuxFileState(h Host, service, timer managed) State {
	state := State{Path: timer.path}
	if foreign := firstForeign(service, timer); foreign != "" {
		state.Foreign = true
		state.Detail = foreignDetail(foreign)
		return state
	}
	if !service.ours || !timer.ours {
		state.Detail = "no sparkwing cron timer is installed here"
		return state
	}
	state.Installed = true
	state.Binary = execStartBinary(service.body)
	state.Stale = staleAgainst(h, state.Binary)
	return state
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

func joinOutput(outs ...string) string {
	var parts []string
	for _, o := range outs {
		if o = strings.TrimSpace(o); o != "" {
			parts = append(parts, o)
		}
	}
	if len(parts) == 0 {
		return "systemctl said nothing"
	}
	return strings.Join(parts, "; ")
}

func serviceUnit(h Host) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", Marker)
	b.WriteString("[Unit]\nDescription=sparkwing crons tick\n\n")
	b.WriteString("[Service]\nType=oneshot\n")
	fmt.Fprintf(&b, "ExecStart=%s crons tick\n", systemdWord(h.Binary))
	for _, kv := range envPairs(h) {
		fmt.Fprintf(&b, "Environment=%s\n", systemdAssignment(kv[0], kv[1]))
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
	b.WriteString("[Unit]\nDescription=sparkwing crons tick every minute\n\n")
	b.WriteString("[Timer]\n")
	b.WriteString("OnCalendar=*-*-* *:*:00\n")
	b.WriteString("AccuracySec=1s\n")
	b.WriteString("Persistent=true\n")
	fmt.Fprintf(&b, "Unit=%s\n\n", ServiceName)
	b.WriteString("[Install]\nWantedBy=timers.target\n")
	return b.String()
}

// safety: systemd splits an argument on whitespace and reads % as a specifier, so quote one and double the other.
func systemdWord(v string) string {
	escaped := strings.ReplaceAll(v, "%", "%%")
	if v == "" {
		return `""`
	}
	if !strings.ContainsAny(escaped, " \t\n\"'\\") {
		return escaped
	}
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(escaped) + `"`
}

// safety: systemd quotes the whole KEY=VALUE assignment, not the value alone.
func systemdAssignment(key, value string) string {
	assignment := key + "=" + strings.ReplaceAll(value, "%", "%%")
	if !strings.ContainsAny(assignment, " \t\n\"'\\") {
		return assignment
	}
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(assignment) + `"`
}

// safety: the unit is the only record of the binary it runs; nothing keeps a sidecar.
func execStartBinary(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !ok {
			continue
		}
		return firstSystemdWord(rest)
	}
	return ""
}

func firstSystemdWord(s string) string {
	s = strings.TrimLeft(s, " \t")
	var b strings.Builder
	if strings.HasPrefix(s, `"`) {
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
				continue
			}
			if s[i] == '"' {
				break
			}
			b.WriteByte(s[i])
		}
	} else {
		for i := 0; i < len(s) && s[i] != ' ' && s[i] != '\t'; i++ {
			b.WriteByte(s[i])
		}
	}
	return strings.ReplaceAll(b.String(), "%%", "%")
}
