package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

const (
	toolchainModeEnv   = "SPARKWING_TOOLCHAIN"
	toolchainActiveEnv = "SPARKWING_TOOLCHAIN_ACTIVE"

	toolchainModeAuto  = "auto"
	toolchainModeLocal = "local"
)

type toolchainAction int

const (
	toolchainStay toolchainAction = iota
	toolchainSwitch
	toolchainRefuse
)

type toolchainDecision struct {
	action    toolchainAction
	installed string
	pin       string
}

type sdkPin struct {
	version string
	replace string
}

// switchToolchain runs the CLI this repo's SDK pin names when the installed CLI
// is older than that pin. On unix a switch replaces this process and never
// returns; on Windows it runs the pinned CLI as a child and exits with its code.
func switchToolchain(sparkwingDir string) error {
	mode, err := toolchainMode(os.Getenv(toolchainModeEnv))
	if err != nil {
		return err
	}
	pin, err := readSDKPin(sparkwingDir)
	if err != nil {
		return nil
	}
	decision := planToolchainSwitch(installedVersion(), pin, mode, os.Getenv(toolchainActiveEnv))
	switch decision.action {
	case toolchainStay:
		return nil
	case toolchainRefuse:
		return toolchainLocalRefusal(decision)
	}
	return runToolchain(os.Stderr, decision)
}

func toolchainMode(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "", toolchainModeAuto:
		return toolchainModeAuto, nil
	case toolchainModeLocal:
		return toolchainModeLocal, nil
	default:
		return "", exitErrorf(2, "%s=%q is not a valid value: use %s (fetch and run the CLI this repo pins) or %s (never fetch)",
			toolchainModeEnv, raw, toolchainModeAuto, toolchainModeLocal)
	}
}

// planToolchainSwitch decides whether the pin outranks the installed CLI. A
// source build on either side wins, because a checkout is what its author is
// testing; only a release tag on both sides can order them.
func planToolchainSwitch(installed string, pin sdkPin, mode, active string) toolchainDecision {
	d := toolchainDecision{installed: installed, pin: pin.version}
	switch {
	case active != "":
		return d
	case pin.replace != "" || pin.version == "":
		return d
	case !isReleaseTag(installed) || !isReleaseTag(pin.version):
		return d
	case semver.Compare(installed, pin.version) >= 0:
		return d
	case mode == toolchainModeLocal:
		d.action = toolchainRefuse
		return d
	}
	d.action = toolchainSwitch
	return d
}

func isReleaseTag(v string) bool {
	return semver.IsValid(v) && semver.Canonical(v) == v &&
		semver.Prerelease(v) == "" && semver.Build(v) == ""
}

func readSDKPin(sparkwingDir string) (sdkPin, error) {
	path := filepath.Join(sparkwingDir, "go.mod")
	body, err := os.ReadFile(path)
	if err != nil {
		return sdkPin{}, err
	}
	mf, err := modfile.Parse(path, body, nil)
	if err != nil {
		return sdkPin{}, err
	}
	var pin sdkPin
	for _, req := range mf.Require {
		if req.Mod.Path == sdkModulePath {
			pin.version = req.Mod.Version
		}
	}
	for _, rep := range mf.Replace {
		if rep.Old.Path != sdkModulePath {
			continue
		}
		pin.replace = rep.New.Path
		if rep.New.Version != "" {
			pin.replace += "@" + rep.New.Version
		}
		break
	}
	return pin, nil
}

func runToolchain(w io.Writer, d toolchainDecision) error {
	binPath, err := ensureToolchainBinary(w, d.pin)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "sparkwing: running %s from %s because this repo pins SDK %s and the installed sparkwing is %s\n",
		d.pin, tildePath(binPath), d.pin, d.installed)
	return execToolchain(binPath, os.Args[1:], setEnv(os.Environ(), toolchainActiveEnv, d.pin))
}

// ensureToolchainBinary returns the store path of the pinned CLI, fetching and
// verifying the release when the store has no binary matching its recorded digest.
func ensureToolchainBinary(w io.Writer, version string) (string, error) {
	p, err := paths.DefaultPaths()
	if err != nil {
		return "", fmt.Errorf("locate the sparkwing home for the toolchain store: %w", err)
	}
	dir := p.ToolchainDir(version)
	binPath := p.ToolchainBinary(version)
	if toolchainCacheHit(binPath) {
		return binPath, nil
	}
	verified, err := fetchVerifiedRelease(version)
	if err != nil {
		return "", toolchainFetchError(version, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create toolchain store %s: %w", dir, err)
	}
	if err := writeToolchainFile(dir, binPath, verified.bytes, 0o755); err != nil {
		return "", err
	}
	if err := writeToolchainFile(dir, toolchainDigestPath(binPath), []byte(verified.digest+"\n"), 0o644); err != nil {
		return "", err
	}
	fmt.Fprintf(w, "sparkwing: fetched and verified sparkwing %s from %s (sha256 %s)\n",
		version, releaseBaseURL(version), verified.digest)
	return binPath, nil
}

func toolchainCacheHit(binPath string) bool {
	recorded, err := os.ReadFile(toolchainDigestPath(binPath))
	if err != nil {
		return false
	}
	actual, err := sha256OfFile(binPath)
	if err != nil {
		return false
	}
	return actual == strings.TrimSpace(string(recorded))
}

func toolchainDigestPath(binPath string) string { return binPath + ".sha256" }

// writeToolchainFile stages beside the destination and renames, so a concurrent
// reader either sees the previous file or the complete new one.
func writeToolchainFile(dir, dest string, body []byte, mode os.FileMode) error {
	tmp, err := writeInstallTemp(dir, ".sparkwing-toolchain-*", body, mode)
	if err != nil {
		return fmt.Errorf("stage %s: %w", dest, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install %s: %w", dest, err)
	}
	return nil
}

func toolchainLocalRefusal(d toolchainDecision) error {
	return fmt.Errorf(
		"this repo pins SDK %s but the installed sparkwing is %s and %s=%s forbids fetching one. "+
			"Install %s with `sparkwing update --version %s`, or unset %s",
		d.pin, d.installed, toolchainModeEnv, toolchainModeLocal, d.pin, d.pin, toolchainModeEnv)
}

func toolchainFetchError(version string, cause error) error {
	return fmt.Errorf(
		"fetch sparkwing %s, the SDK version this repo pins: %w\n"+
			"  tried %s\n"+
			"  install it by hand with `sparkwing update --version %s` and re-run",
		version, cause, releaseBaseURL(version), version)
}

func tildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	prefix := home + string(os.PathSeparator)
	if strings.HasPrefix(path, prefix) {
		return "~" + string(os.PathSeparator) + strings.TrimPrefix(path, prefix)
	}
	return path
}
