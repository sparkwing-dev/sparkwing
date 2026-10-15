package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Inspect reads the installed command without contacting the service manager or
// reading credentials. It reports registrations for another configuration as Mismatch.
func Inspect(ctx context.Context, h Host) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	path, err := registrationPath(h)
	if err != nil {
		return State{}, err
	}
	existing, err := readManaged(path)
	if err != nil {
		return State{}, err
	}
	if existing.foreign() {
		return foreignState(path), nil
	}
	state := State{Path: path, Installed: existing.exists}
	if !existing.exists {
		return state, nil
	}
	args := unitArguments(existing.body)
	if h.GOOS == "darwin" {
		args = plistArguments([]byte(existing.body))
	}
	if len(args) < 2 || !filepath.IsAbs(args[0]) {
		return state, fmt.Errorf("runner service: unreadable command in %s", path)
	}
	state.Binary, state.Arguments = args[0], slices.Clone(args[1:])
	if config := configArgument(args); !sameConfig(config, h.ConfigPath) {
		state.Mismatch = true
		state.Detail = mismatchState(path, config, h.ConfigPath).Detail
	}
	return state, nil
}

// Refresh rewrites an existing registration and restarts it if active. It never
// installs or enables a missing service, opens configuration, or touches a foreign
// registration. Call it from outside the runner service: launchd reloads stop its
// process before bootstrapping the replacement. Self-updaters need an external owner.
func Refresh(ctx context.Context, h Host) (State, error) {
	state, err := Inspect(ctx, h)
	if err != nil {
		return state, err
	}
	if state.Foreign {
		return state, foreignError(state.Path)
	}
	if state.Mismatch {
		return state, errors.New(state.Detail)
	}
	if !state.Installed {
		return state, nil
	}
	if err := requireHost(h); err != nil {
		return state, err
	}
	if h.Exec == nil {
		return state, errors.New("runner service: Host.Exec is required")
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	body := serviceUnit(h)
	if h.GOOS == "darwin" {
		body = agentPlist(h)
		if err := ensureLogDir(h.LogPath); err != nil {
			return state, err
		}
	}
	if err := writeManaged(state.Path, body); err != nil {
		return state, err
	}
	state.Binary, state.Arguments = h.Binary, []string{"start", "--config", h.ConfigPath}
	if h.GOOS == "linux" {
		if err := runRefresh(ctx, h, "systemctl", "--user", "daemon-reload"); err != nil {
			return state, err
		}
		// safety: updating software must not start a registration its operator stopped.
		return state, runRefresh(ctx, h, "systemctl", "--user", "try-restart", ServiceName)
	}
	out, err := h.run(ctx, "launchctl", "print", h.serviceTarget())
	if err != nil {
		if ctx.Err() == nil && notLoaded(out) {
			return state, nil
		}
		return state, fmt.Errorf("runner service: inspect active launchd registration: %w: %s", err, strings.TrimSpace(out))
	}
	running, err := launchdRunning(out)
	if err != nil || !running {
		return state, err
	}
	if err := runRefresh(ctx, h, "launchctl", "bootout", h.serviceTarget()); err != nil {
		return state, err
	}
	if err := runRefresh(ctx, h, "launchctl", "bootstrap", h.domainTarget(), state.Path); err != nil {
		return state, err
	}
	state.Running = true
	return state, nil
}

func launchdRunning(output string) (bool, error) {
	for _, line := range strings.Split(output, "\n") {
		if state, ok := strings.CutPrefix(strings.TrimSpace(line), "state = "); ok {
			return state == "running", nil
		}
	}
	return false, errors.New("runner service: launchctl did not report the registration's state")
}

func registrationPath(h Host) (string, error) {
	switch h.GOOS {
	case "linux":
		if !filepath.IsAbs(h.ConfigHome) {
			return "", errors.New("runner service: Host.ConfigHome must be absolute")
		}
		return UnitPath(h), nil
	case "darwin":
		if !filepath.IsAbs(h.Home) {
			return "", errors.New("runner service: Host.Home must be absolute")
		}
		return PlistPath(h), nil
	default:
		return "", unsupported(h.GOOS)
	}
}

func runRefresh(ctx context.Context, h Host, name string, args ...string) error {
	out, err := h.run(ctx, name, args...)
	if err != nil {
		return fmt.Errorf("runner service: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return nil
}
