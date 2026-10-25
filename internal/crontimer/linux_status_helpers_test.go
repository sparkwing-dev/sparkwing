package crontimer

import (
	"context"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

func statusLinux(ctx context.Context, h Host) (State, error) {
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
	if timerHome(unitfile.Arguments("linux", service.body)) != h.SparkwingHome {
		state.Detail = otherHomeDetail(h, servicePath)
		return state, nil
	}
	if loaded := loadedElsewhere(ctx, h, timerPath); loaded != "" {
		state.Detail = fmt.Sprintf("systemd runs %s from %s, not from %s", h.timerName(), loaded, timerPath)
		return state, nil
	}
	enabledOut, enabledErr := h.run(ctx, "systemctl", "--user", "is-enabled", h.timerName())
	activeOut, activeErr := h.run(ctx, "systemctl", "--user", "is-active", h.timerName())
	switch {
	case enabledErr == nil && activeErr == nil:
		state.Enabled = true
		state.Detail = fmt.Sprintf("%s is enabled and active, running %s __tick every minute", h.timerName(), state.Binary)
	default:
		state.Detail = fmt.Sprintf("%s is installed but systemd does not report it running: %s", h.timerName(),
			joinOutput(enabledOut, activeOut))
	}
	if state.Stale {
		state.Detail += fmt.Sprintf("; it runs %s, not %s", state.Binary, h.Binary)
	}
	return state, nil
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
	return commandState(h, timer.path, service.body)
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
