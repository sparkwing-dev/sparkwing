package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

const (
	toolchainModeEnv   = "SPARKWING_TOOLCHAIN"
	toolchainActiveEnv = "SPARKWING_TOOLCHAIN_ACTIVE"

	toolchainModeAuto  = "auto"
	toolchainModeLocal = "local"

	releaseManifestName = "SHA256SUMS"
)

var toolchainActive string

var toolchainExecFn = execToolchain

func takeToolchainActive() string {
	value := strings.TrimSpace(os.Getenv(toolchainActiveEnv))
	_ = os.Unsetenv(toolchainActiveEnv)
	return value
}

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

func switchToolchain(sparkwingDir string) error {
	if err := checkToolchainIdentity(); err != nil {
		return err
	}
	mode, err := toolchainMode(os.Getenv(toolchainModeEnv))
	if err != nil {
		return err
	}
	pin, err := readSDKPin(sparkwingDir)
	if err != nil {
		return nil
	}
	decision := planToolchainSwitch(installedVersion(), pin, mode, toolchainActive)
	switch decision.action {
	case toolchainStay:
		return nil
	case toolchainRefuse:
		return toolchainLocalRefusal(decision)
	}
	return runToolchain(os.Stderr, decision)
}

func checkToolchainIdentity() error {
	if toolchainActive == "" {
		return nil
	}
	if installed := installedVersion(); isReleaseTag(installed) && installed != toolchainActive {
		return fmt.Errorf(
			"switched to sparkwing %s but this binary reports %s; the toolchain store entry for %s holds another release. "+
				"Delete %s and re-run",
			toolchainActive, installed, toolchainActive, tildePath(toolchainStorePath(toolchainActive)))
	}
	return nil
}

func toolchainStorePath(version string) string {
	p, err := paths.DefaultPaths()
	if err != nil {
		return filepath.Join("$SPARKWING_HOME", "toolchains", version)
	}
	return p.ToolchainDir(version)
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

func planToolchainSwitch(installed string, pin sdkPin, mode, active string) toolchainDecision {
	d := toolchainDecision{installed: installed, pin: pin.version}
	switch {
	case active != "" && active == pin.version:
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
	return toolchainExecFn(binPath, os.Args[1:], setEnv(os.Environ(), toolchainActiveEnv, d.pin))
}

func ensureToolchainBinary(w io.Writer, version string) (string, error) {
	if !isReleaseTag(version) {
		return "", fmt.Errorf("toolchain version %q is not a canonical stable release", version)
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		return "", fmt.Errorf("locate the sparkwing home for the toolchain store: %w", err)
	}
	dir := p.ToolchainDir(version)
	binPath := p.ToolchainBinary(version)
	stale := ""
	if verifyErr := verifyStoredToolchain(dir, binPath); verifyErr == nil {
		removeLegacyDigestSidecar(binPath)
		return binPath, nil
	} else if _, statErr := os.Stat(binPath); statErr == nil {
		stale = verifyErr.Error()
		fmt.Fprintf(w, "sparkwing: stored toolchain %s failed verification (%s); fetching again\n", version, stale)
	}
	verified, err := fetchVerifiedRelease(version)
	if err != nil {
		return "", toolchainFetchError(version, stale, err)
	}
	if err := prepareToolchainStore(p, dir); err != nil {
		return "", err
	}
	stage, err := writeInstallTemp(dir, ".sparkwing-toolchain-*", verified.bytes, 0o700)
	if err != nil {
		return "", fmt.Errorf("stage %s: %w", binPath, err)
	}
	if err := assertToolchainVersion(stage, version); err != nil {
		_ = os.Remove(stage)
		return "", err
	}
	if err := os.Rename(stage, binPath); err != nil {
		_ = os.Remove(stage)
		return "", fmt.Errorf("install %s: %w", binPath, err)
	}
	if err := writeToolchainFile(dir, filepath.Join(dir, releaseManifestName), verified.manifest); err != nil {
		return "", err
	}
	if err := writeToolchainFile(dir, filepath.Join(dir, releaseManifestName+".sig"), verified.manifestSig); err != nil {
		return "", err
	}
	removeLegacyDigestSidecar(binPath)
	fmt.Fprintf(w, "sparkwing: fetched and verified sparkwing %s from %s (sha256 %s)\n",
		version, releaseBaseURL(version), verified.digest)
	return binPath, nil
}

func removeLegacyDigestSidecar(binPath string) {
	_ = os.Remove(binPath + ".sha256")
}

func prepareToolchainStore(p paths.Paths, dir string) error {
	if err := p.EnsureRoot(); err != nil {
		return fmt.Errorf("prepare sparkwing home %s: %w", p.Root, err)
	}
	if err := fssecure.EnsureDir(p.ToolchainsDir()); err != nil {
		return fmt.Errorf("create toolchain store %s: %w", p.ToolchainsDir(), err)
	}
	if err := fssecure.EnsureDir(dir); err != nil {
		return fmt.Errorf("create toolchain store %s: %w", dir, err)
	}
	return nil
}

func verifyStoredToolchain(dir, binPath string) error {
	// #nosec G703 -- the caller admits only canonical stable release names beneath the private toolchain store
	manifest, err := os.ReadFile(filepath.Join(dir, releaseManifestName))
	if err != nil {
		return err
	}
	// #nosec G703 -- the caller admits only canonical stable release names beneath the private toolchain store
	manifestSig, err := os.ReadFile(filepath.Join(dir, releaseManifestName+".sig"))
	if err != nil {
		return err
	}
	publicKeys, err := releasePublicKeys()
	if err != nil {
		return err
	}
	if !manifestSignedByTrustSet(publicKeys, manifest, manifestSig) {
		return fmt.Errorf("%s in %s does not carry a signature from the updater trust set", releaseManifestName, dir)
	}
	want, err := manifestDigest(manifest, releaseAssetName())
	if err != nil {
		return err
	}
	got, err := sha256OfFile(binPath)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("stored %s has digest %s, but %s records %s", binPath, got, releaseManifestName, want)
	}
	return ensureToolchainExecutable(binPath)
}

func ensureToolchainExecutable(binPath string) error {
	info, err := os.Stat(binPath)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o100 != 0 {
		return nil
	}
	if err := os.Chmod(binPath, 0o700); err != nil {
		return fmt.Errorf("restore the execute bit on %s: %w", binPath, err)
	}
	return nil
}

func assertToolchainVersion(binPath, version string) error {
	args := []string{"version", "-o", "json", "--offline"}
	// #nosec G702 -- the release binary this process just verified against its signed manifest, asked only for its version
	out, err := exec.Command(binPath, args...).Output()
	if err != nil {
		return fmt.Errorf("ask the fetched sparkwing %s for its version: `%s %s`: %w%s",
			version, binPath, strings.Join(args, " "), err, childStderr(err))
	}
	var report VersionReport
	if err := json.Unmarshal(out, &report); err != nil {
		return fmt.Errorf("parse the version the fetched sparkwing %s reports: %w", version, err)
	}
	if report.CLI.Installed != version {
		return fmt.Errorf(
			"the release published as %s reports itself as %s; refusing to cache it as %s",
			version, report.CLI.Installed, version)
	}
	return nil
}

func childStderr(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return ""
	}
	text := strings.TrimSpace(string(exitErr.Stderr))
	if text == "" {
		return ""
	}
	return "\n  it printed: " + text
}

// safety: rename is the only atomic step; a concurrent reader must never see a partial file.
func writeToolchainFile(dir, dest string, body []byte) error {
	tmp, err := writeInstallTemp(dir, ".sparkwing-toolchain-*", body, fssecure.FileMode)
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

func toolchainFetchError(version, stale string, cause error) error {
	reason := ""
	if stale != "" {
		reason = "\n  the copy already in the store was rejected: " + stale
	}
	return fmt.Errorf(
		"fetch sparkwing %s, the SDK version this repo pins: %w%s\n"+
			"  tried %s\n"+
			"  install it by hand with `sparkwing update --version %s` and re-run",
		version, cause, reason, releaseBaseURL(version), version)
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
