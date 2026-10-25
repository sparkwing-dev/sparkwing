package crontimer

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

func statusDarwin(ctx context.Context, h Host) (State, error) {
	path, err := darwinPlistPath(h)
	if err != nil {
		return State{}, err
	}
	plist, err := readManaged(path)
	if err != nil {
		return State{}, err
	}
	state := State{Path: path}
	switch {
	case plist.exists && !plist.ours:
		state.Foreign = true
		state.Detail = foreignDetail(path)
		return state, nil
	case !plist.exists:
		state.Detail = "no sparkwing cron agent is installed here"
		return state, nil
	}
	state = commandState(h, path, plist.body)
	if timerHome(unitfile.PlistArguments([]byte(plist.body))) != h.SparkwingHome {
		state.Detail = otherHomeDetail(h, path)
		return state, nil
	}

	out, err := h.run(ctx, "launchctl", "print", h.serviceTarget())
	if err == nil {
		state.Enabled = true
		state.Detail = fmt.Sprintf("%s is loaded and runs %s __tick every minute", h.label(), state.Binary)
	} else {
		state.Detail = fmt.Sprintf("%s is installed but launchd does not report it loaded: %s", h.label(), joinOutput(out))
	}
	if state.Stale {
		state.Detail += fmt.Sprintf("; it runs %s, not %s", state.Binary, h.Binary)
	}
	return state, nil
}
