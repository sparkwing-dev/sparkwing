package main

import (
	"encoding/json"
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

// toolchainActive carries the version this process was switched to, read once at
// startup. main() clears the variable from the environment so the pipeline binary
// and the daemon it spawns do not inherit a guard meant for this process.
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

// switchToolchain runs the CLI this repo's SDK pin names when the installed CLI
// is older than that pin. On unix a switch replaces this process and never
// returns; on Windows it runs the pinned CLI as a child and exits with its code.
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

// checkToolchainIdentity refuses to keep running when the release the parent
// switched to does not identify as the version the parent announced, which is
// the one way a poisoned store can put a mislabelled release behind the notice.
// A build that is not a release did not come out of the store, so the value came
// from somewhere else and the ordinary comparison decides.
func checkToolchainIdentity() error {
	if toolchainActive == "" {
		return nil
	}
	if installed := installedVersion(); isReleaseTag(installed) && installed != toolchainActive {
		return fmt.Errorf(
			"switched to sparkwing %s but this binary reports %s; the toolchain store entry for %s holds another release. "+
				"Delete $SPARKWING_HOME/toolchains/%s and re-run",
			toolchainActive, installed, toolchainActive, toolchainActive)
	}
	return nil
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

// ensureToolchainBinary returns the store path of the pinned CLI. A stored
// release is used only when the signed release manifest beside it still vouches
// for its bytes; anything else fetches the release again.
func ensureToolchainBinary(w io.Writer, version string) (string, error) {
	p, err := paths.DefaultPaths()
	if err != nil {
		return "", fmt.Errorf("locate the sparkwing home for the toolchain store: %w", err)
	}
	dir := p.ToolchainDir(version)
	binPath := p.ToolchainBinary(version)
	if err := verifyStoredToolchain(dir, binPath); err == nil {
		return binPath, nil
	}
	verified, err := fetchVerifiedRelease(version)
	if err != nil {
		return "", toolchainFetchError(version, err)
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
	fmt.Fprintf(w, "sparkwing: fetched and verified sparkwing %s from %s (sha256 %s)\n",
		version, releaseBaseURL(version), verified.digest)
	return binPath, nil
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

// verifyStoredToolchain re-runs the release check offline: a trust-set key must
// have signed the manifest beside the binary, and the manifest must name the
// binary's own digest. It repairs an executable bit a permission sweep clamped,
// which is cheaper than refetching the release over it.
func verifyStoredToolchain(dir, binPath string) error {
	manifest, err := os.ReadFile(filepath.Join(dir, releaseManifestName))
	if err != nil {
		return err
	}
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

// assertToolchainVersion refuses to cache a release that does not identify as the
// pin, so a retagged manifest or a poisoned mirror cannot hide behind the notice.
func assertToolchainVersion(binPath, version string) error {
	// #nosec G702 -- the release binary this process just verified against its signed manifest, asked only for its version
	out, err := exec.Command(binPath, "version", "-o", "json", "--offline").Output()
	if err != nil {
		return fmt.Errorf("ask the fetched sparkwing %s for its version: %w", version, err)
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

// writeToolchainFile stages beside the destination and renames, so a concurrent
// reader either sees the previous file or the complete new one.
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
