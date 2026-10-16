package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

// Inspect reads the registration command and configuration without contacting its service manager.
func Inspect(ctx context.Context, h Host) (State, error) {
	state, _, err := inspect(ctx, h)
	return state, err
}

func inspect(ctx context.Context, h Host) (State, string, error) {
	if err := ctx.Err(); err != nil {
		return State{}, "", err
	}
	path, err := registrationPath(h)
	if err != nil {
		return State{}, "", err
	}
	body, exists, managed, err := unitfile.Read(path, Marker)
	if err != nil {
		return State{}, "", err
	}
	if exists && !managed {
		return foreignState(path), body, nil
	}
	state := State{Path: path, Installed: exists}
	if !exists {
		return state, body, nil
	}
	args := unitfile.Arguments(h.GOOS, body)
	if len(args) < 2 || !filepath.IsAbs(args[0]) {
		return state, body, fmt.Errorf("runner service: unreadable command in %s", path)
	}
	state.Binary, state.Arguments = args[0], slices.Clone(args[1:])
	if config := configArgument(args); config == "" || config != h.ConfigPath {
		state.Mismatch = true
		if config == "" {
			config = "(none it could read)"
		}
		state.Detail = fmt.Sprintf("%s runs the runner config %s, not %s, so sparkwing left it alone", path, config, h.ConfigPath)
	}
	return state, body, nil
}

// RefreshRegistration reloads a matching installed registration without restarting its process.
func RefreshRegistration(ctx context.Context, h Host) error {
	state, err := Inspect(ctx, h)
	if err != nil {
		return err
	}
	if state.Foreign {
		return foreignError(state.Path)
	}
	if state.Mismatch {
		return errors.New(state.Detail)
	}
	if !state.Installed {
		return nil
	}
	if state.Binary != h.Binary {
		return errors.New("runner registration points to another executable")
	}
	if err := requireHost(h); err != nil {
		return err
	}
	if _, _, err := h.registration(ctx, state.Path); err != nil {
		return err
	}
	if h.GOOS == "linux" {
		return runCommand(ctx, h, "systemctl", "--user", "daemon-reload")
	}
	return context.Cause(ctx)
}

// Refresh updates a matching registration and restarts it if active, from outside the service.
func Refresh(ctx context.Context, h Host) (State, error) {
	state, body, err := inspect(ctx, h)
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
	loaded, agentStatus, err := h.registration(ctx, state.Path)
	if err != nil {
		return state, err
	}
	body, err = unitfile.RewriteCommand(h.GOOS, body, append([]string{h.Binary}, state.Arguments...), unitfile.Version(body))
	if err != nil {
		return state, err
	}
	if err := unitfile.Write(state.Path, body); err != nil {
		return state, err
	}
	state.Binary = h.Binary
	if h.GOOS == "linux" {
		if err := runCommand(ctx, h, "systemctl", "--user", "daemon-reload"); err != nil {
			return state, err
		}
		// safety: updating software must not start a registration its operator stopped.
		return state, runCommand(ctx, h, "systemctl", "--user", "try-restart", ServiceName)
	}
	if !loaded {
		return state, nil
	}
	running, err := launchdRunning(agentStatus)
	if err != nil || !running {
		return state, err
	}
	if err := runCommand(ctx, h, "launchctl", "bootout", h.serviceTarget()); err != nil {
		return state, err
	}
	if err := runCommand(ctx, h, "launchctl", "bootstrap", h.domainTarget(), state.Path); err != nil {
		return state, err
	}
	state.Running = true
	return state, nil
}

func agentRegistration(ctx context.Context, h Host) (bool, string, error) {
	out, err := h.run(ctx, "launchctl", "print", h.serviceTarget())
	if err != nil && ctx.Err() == nil && notLoaded(out) {
		return false, out, nil
	}
	if err != nil {
		return false, out, fmt.Errorf("runner service: inspect launchd registration: %w: %s", err, strings.TrimSpace(out))
	}
	if err := unitfile.CheckAgentSource(out, PlistPath(h)); err != nil {
		return false, out, err
	}
	return true, out, nil
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

func runCommand(ctx context.Context, h Host, name string, args ...string) error {
	out, err := h.run(ctx, name, args...)
	if err != nil {
		return fmt.Errorf("runner service: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return nil
}
