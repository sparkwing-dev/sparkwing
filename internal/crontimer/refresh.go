package crontimer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runners/service/unitfile"
)

func checkOperation(ctx context.Context, h Host) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.GOOS != "linux" && h.GOOS != "darwin" {
		return unsupported(h.GOOS)
	}
	if !filepath.IsAbs(h.SparkwingHome) || !filepath.IsAbs(h.Home) || (h.GOOS == "linux" && !filepath.IsAbs(h.ConfigHome)) {
		return errors.New("crontimer: SparkwingHome, Home and the native unit directory must be absolute")
	}
	return nil
}

func timerCommand(h Host) []string {
	return []string{h.Binary, "__tick", "--home", h.SparkwingHome}
}

func systemdCommand(h Host) string {
	return unitfile.Command(timerCommand(h))
}

func binaryArgument(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func timerHome(args []string) string {
	if len(args) != 4 || args[1] != "__tick" || args[2] != "--home" || !filepath.IsAbs(args[3]) {
		return ""
	}
	return args[3]
}

func commandState(h Host, path, body string) State {
	args := unitfile.Arguments(h.GOOS, body)
	state := State{Installed: true, Path: path, Binary: binaryArgument(args), Home: timerHome(args)}
	if len(args) > 0 {
		state.Arguments = slices.Clone(args[1:])
	}
	state.Stale = staleAgainst(h, state.Binary) || state.Home != h.SparkwingHome
	state.Version = unitfile.Version(body)
	return state
}

func managedFiles(h Host) (driver, companion managed, err error) {
	if h.GOOS == "linux" {
		servicePath, timerPath, err := linuxPaths(h)
		if err != nil {
			return managed{}, managed{}, err
		}
		return readLinuxFiles(servicePath, timerPath)
	}
	path, err := darwinPlistPath(h)
	if err == nil {
		driver, err = readManaged(path)
	}
	return driver, managed{}, err
}

// Inspect reads the native command and release without opening the store or contacting a service manager.
func Inspect(ctx context.Context, h Host) (State, error) {
	if err := checkOperation(ctx, h); err != nil {
		return State{}, err
	}
	driver, companion, err := managedFiles(h)
	if err != nil {
		return State{}, err
	}
	path := driver.path
	if h.GOOS == "linux" {
		path = companion.path
	}
	if foreign := firstForeign(driver, companion); foreign != "" {
		return State{Path: path, Foreign: true, Detail: foreignDetail(foreign)}, nil
	}
	if !driver.exists && !companion.exists {
		return State{Path: path}, nil
	}
	if !driver.exists || (h.GOOS == "linux" && !companion.exists) {
		return State{}, errors.New("crontimer: incomplete native timer registration; reinstall schedules")
	}
	return commandState(h, path, driver.body), nil
}

// Refresh updates an existing timer's executable and release while retaining its captured settings.
// Missing timers stay absent. Installed launchd agents reload; systemd timers keep their enabled state.
func Refresh(ctx context.Context, h Host) (State, error) {
	state, err := Inspect(ctx, h)
	if err != nil {
		return state, err
	}
	if state.Foreign || (state.Installed && state.Home != h.SparkwingHome) {
		return state, errors.New("crontimer: refusing to refresh a foreign or different home's timer")
	}
	if !state.Installed {
		return state, nil
	}
	if err := requireBinary(h); err != nil {
		return state, err
	}
	driver, _, err := managedFiles(h)
	if err != nil {
		return state, err
	}
	body, err := unitfile.RewriteCommand(h.GOOS, driver.body, timerCommand(h), h.Version)
	if err != nil {
		return state, err
	}
	loaded, err := refreshPreflight(ctx, h, state.Path)
	if err != nil {
		return state, err
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	if err := writeManaged(driver.path, body); err != nil {
		return state, err
	}
	state = commandState(h, state.Path, body)
	if h.GOOS == "linux" {
		return state, runRefresh(ctx, h, "systemctl", "--user", "daemon-reload")
	}
	if loaded {
		if err := runRefresh(ctx, h, "launchctl", "bootout", h.serviceTarget()); err != nil {
			return state, err
		}
	}
	return state, runRefresh(ctx, h, "launchctl", "bootstrap", h.domainTarget(), state.Path)
}

func refreshPreflight(ctx context.Context, h Host, path string) (bool, error) {
	if h.GOOS == "linux" {
		service, _, err := linuxPaths(h)
		if err == nil {
			err = unitfile.CheckSystemdSource(ctx, h.Exec, h.serviceName(), service)
		}
		if err == nil {
			err = unitfile.CheckSystemdSource(ctx, h.Exec, h.timerName(), path)
		}
		return true, err
	}
	out, err := h.run(ctx, "launchctl", "print", h.serviceTarget())
	if err != nil && ctx.Err() == nil && notLoaded(out) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("crontimer: inspect agent registration: %w: %s", err, strings.TrimSpace(out))
	}
	return true, unitfile.CheckAgentSource(out, path)
}

func runRefresh(ctx context.Context, h Host, name string, args ...string) error {
	out, err := h.run(ctx, name, args...)
	if err != nil {
		return fmt.Errorf("crontimer: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return nil
}
