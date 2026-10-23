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
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	path, err := registrationPath(h)
	if err != nil {
		return State{}, err
	}
	body, exists, managed, err := unitfile.Read(path, marker)
	if err != nil {
		return State{}, err
	}
	if exists && !managed {
		return foreignState(path), nil
	}
	state := State{Path: path, Installed: exists}
	if !exists {
		return state, nil
	}
	args := unitfile.Arguments(h.GOOS, body)
	if len(args) < 2 || !filepath.IsAbs(args[0]) {
		return state, fmt.Errorf("runner service: unreadable command in %s", path)
	}
	state.Binary, state.Arguments = args[0], slices.Clone(args[1:])
	if config := configArgument(args); config == "" || config != h.ConfigPath {
		state.Mismatch = true
		if config == "" {
			config = "(none it could read)"
		}
		state.Detail = fmt.Sprintf("%s runs the runner config %s, not %s, so sparkwing left it alone", path, config, h.ConfigPath)
	}
	return state, nil
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
	if _, err := h.registration(ctx, state.Path); err != nil {
		return err
	}
	if h.GOOS == "linux" {
		return runCommand(ctx, h, "systemctl", "--user", "daemon-reload")
	}
	return context.Cause(ctx)
}

func agentRegistration(ctx context.Context, h Host) (bool, error) {
	out, err := h.run(ctx, "launchctl", "print", h.serviceTarget())
	if err != nil && ctx.Err() == nil && notLoaded(out) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("runner service: inspect launchd registration: %w: %s", err, strings.TrimSpace(out))
	}
	if err := unitfile.CheckAgentSource(out, plistPath(h)); err != nil {
		return false, err
	}
	return true, nil
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
		return plistPath(h), nil
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
