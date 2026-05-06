// CLI self-update verbs. Mirrors install.sh: fetch the bare per-platform
// binary, verify SHA256, atomic-rename onto the running binary. macOS
// gets ad-hoc codesigning; Windows uses a rename-aside dance.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
	"golang.org/x/mod/semver"
)

const (
	updateRepo      = "sparkwing-dev/sparkwing"
	updateAssetBase = "https://github.com/" + updateRepo + "/releases/download"
)

// runUpdate is the top-level binary self-update verb (CLI only; for
// SDK pins, see `sparkwing version update --sdk`).
func runUpdate(args []string) error {
	fs := flag.NewFlagSet(cmdUpdate.Path, flag.ContinueOnError)
	check := fs.Bool("check", false, "report current vs latest; exit 1 if a newer release exists")
	force := fs.Bool("force", false, "allow downgrading to an older release")
	version := fs.String("version", "", "target release tag (e.g. v0.17.0). Default: latest.")
	if err := parseAndCheck(cmdUpdate, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("update: unexpected positional %q", fs.Arg(0))
	}
	if *check {
		return runUpdateCheck()
	}
	return runUpdateBinary(*version, *force)
}

func runUpdateCheck() error {
	current := installedVersion()
	latest, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("update --check: could not fetch latest version: %w", err)
	}

	switch {
	case current == "(devel)" || current == "(unknown)":
		fmt.Fprintf(os.Stdout, "installed: %s (dev build, cannot compare)\n", current)
		fmt.Fprintf(os.Stdout, "latest:    %s\n", latest)
		return nil
	case semver.Compare(current, latest) >= 0:
		fmt.Fprintf(os.Stdout, "sparkwing %s is up to date (latest: %s)\n", current, latest)
		return nil
	default:
		fmt.Fprintf(os.Stdout, "sparkwing %s is behind -- latest is %s\n", current, latest)
		fmt.Fprintf(os.Stdout, "run: sparkwing update\n")
		return exitErrorf(1, "newer version available: %s (installed: %s)", latest, current)
	}
}

// runUpdateBinary downloads + verifies + atomically installs.
// Falls back to `go install` when the download fails.
func runUpdateBinary(version string, force bool) error {
	resolved := strings.TrimSpace(version)
	if resolved == "" {
		v, err := fetchLatestRelease()
		if err != nil {
			fmt.Fprintf(os.Stderr, "update: could not fetch latest version (%v); falling back to go install\n", err)
			return updateCLIViaGoInstall("latest")
		}
		resolved = v
	}

	current := installedVersion()

	if current != "(unknown)" && current != "(devel)" && resolved == current {
		fmt.Fprintf(os.Stdout, "sparkwing is already at %s\n", current)
		return nil
	}

	// Require --force to downgrade.
	if !force && isSemver(current) && isSemver(resolved) {
		if semver.Compare(resolved, current) < 0 {
			return fmt.Errorf(
				"update: %s is older than the installed %s\n  to downgrade, re-run with --force",
				resolved, current,
			)
		}
	}

	currentBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	currentBin, _ = filepath.EvalSymlinks(currentBin)

	fmt.Fprintf(os.Stdout, "updating sparkwing: %s -> %s\n", current, resolved)

	if err := downloadAndInstall(resolved, currentBin); err != nil {
		fmt.Fprintf(os.Stderr, "update: download path failed (%v); falling back to go install\n", err)
		return updateCLIViaGoInstall(resolved)
	}

	fmt.Fprintf(os.Stdout, "sparkwing updated: %s -> %s\n", current, resolved)
	fmt.Fprintf(os.Stdout, "what's new: https://github.com/sparkwing-dev/sparkwing/releases\n")
	return nil
}

// runVersionUpdate dispatches `sparkwing version update`. Requires
// exactly one of --cli or --sdk so it can't silently flip the wrong half.
func runVersionUpdate(args []string) error {
	fs := flag.NewFlagSet(cmdVersionUpdate.Path, flag.ContinueOnError)
	cli := fs.Bool("cli", false, "self-update the sparkwing CLI binary")
	sdk := fs.Bool("sdk", false, "bump the SDK pin in this project's .sparkwing/go.mod")
	version := fs.String("version", "", "target release (e.g. v0.17.0). Default: latest.")
	force := fs.Bool("force", false, "allow downgrading to an older release")
	if err := parseAndCheck(cmdVersionUpdate, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("version update: unexpected positional %q", fs.Arg(0))
	}
	switch {
	case *cli && *sdk:
		return errors.New("version update: --cli and --sdk are mutually exclusive")
	case *cli:
		return runUpdateBinary(*version, *force)
	case *sdk:
		return runUpdateSDK(*version)
	default:
		return errors.New("version update: must pass --cli (binary) or --sdk (per-project go.mod pin)")
	}
}

// installedVersion prefers the ldflag-injected main.Version (survives
// -trimpath, no "+dirty" suffix) then runtime/debug.ReadBuildInfo.
func installedVersion() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" {
			return v
		}
	}
	return "(unknown)"
}

// Version is injected via -ldflags="-X main.Version=vX.Y.Z" at release.
var Version string

// downloadAndInstall fetches binary + SHA256SUMS, verifies, atomic-renames.
// Ad-hoc codesigns on macOS to avoid arm64 SIGKILL on first run.
func downloadAndInstall(version, currentBin string) error {
	suffix := runtime.GOOS + "-" + runtime.GOARCH
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	asset := "sparkwing-" + suffix + ext
	base := updateAssetBase + "/" + version

	tmpDir, err := os.MkdirTemp("", "sparkwing-update-")
	if err != nil {
		return fmt.Errorf("mkdir tmp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	binPath := filepath.Join(tmpDir, asset)
	if err := downloadFile(base+"/"+asset, binPath); err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	sumsPath := filepath.Join(tmpDir, "SHA256SUMS")
	if err := downloadFile(base+"/SHA256SUMS", sumsPath); err != nil {
		return fmt.Errorf("download SHA256SUMS: %w", err)
	}

	// Hard-fail on missing/stale manifest — skipping would be a supply-chain foot-gun.
	expected, err := lookupSHA256(sumsPath, asset)
	if err != nil {
		return err
	}
	actual, err := sha256OfFile(binPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("checksum mismatch for %s\n  expected: %s\n  actual:   %s", asset, expected, actual)
	}

	// Stage same-fs so the final rename is atomic (cross-fs renames EXDEV).
	stagedBin := currentBin + ".update.tmp"
	if err := copyFile(binPath, stagedBin); err != nil {
		return fmt.Errorf("stage new binary: %w", err)
	}
	if err := os.Chmod(stagedBin, 0o755); err != nil {
		_ = os.Remove(stagedBin)
		return err
	}

	// macOS ad-hoc codesign; best-effort.
	if runtime.GOOS == "darwin" {
		_ = exec.Command("codesign", "--force", "--sign", "-", stagedBin).Run()
	}

	if err := replaceRunningBinary(stagedBin, currentBin); err != nil {
		_ = os.Remove(stagedBin)
		return fmt.Errorf("replace binary: %w", err)
	}

	// Windows wing.exe is a separate copy (no symlinks); refresh it.
	if runtime.GOOS == "windows" {
		refreshWingSibling(binPath, currentBin)
	}
	return nil
}

// refreshWingSibling refreshes wing.exe alongside sparkwing.exe; best-effort.
func refreshWingSibling(newBinSrc, currentBin string) {
	wingPath := filepath.Join(filepath.Dir(currentBin), "wing.exe")
	if _, err := os.Stat(wingPath); err != nil {
		return
	}
	staged := wingPath + ".update.tmp"
	if err := copyFile(newBinSrc, staged); err != nil {
		return
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		_ = os.Remove(staged)
		return
	}
	if err := replaceRunningBinary(staged, wingPath); err != nil {
		_ = os.Remove(staged)
	}
}

// replaceRunningBinary atomically swaps in the new binary. Windows
// uses a rename-aside dance: cleanupStaleUpdate deletes the .old at
// next launch (the running .exe can't be deleted while executing).
func replaceRunningBinary(stagedBin, currentBin string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(stagedBin, currentBin)
	}
	oldBin := currentBin + ".old"
	_ = os.Remove(oldBin)
	if err := os.Rename(currentBin, oldBin); err != nil {
		return fmt.Errorf("move running binary aside: %w", err)
	}
	if err := os.Rename(stagedBin, currentBin); err != nil {
		_ = os.Rename(oldBin, currentBin)
		return fmt.Errorf("install new binary: %w", err)
	}
	return nil
}

// cleanupStaleUpdate removes <self>.old left by a Windows self-update.
func cleanupStaleUpdate() {
	if runtime.GOOS != "windows" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	_ = os.Remove(self + ".old")
}

func downloadFile(url, dst string) error {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func lookupSHA256(sumsPath, filename string) (string, error) {
	body, err := os.ReadFile(sumsPath)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == filename {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s not listed in SHA256SUMS", filename)
}

func sha256OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// updateCLIViaGoInstall is the go-toolchain fallback when download fails.
func updateCLIViaGoInstall(version string) error {
	target := "github.com/" + updateRepo + "/cmd/sparkwing@"
	if version == "" || version == "latest" {
		target += "latest"
	} else {
		target += version
	}
	fmt.Fprintf(os.Stdout, "go install -ldflags=\"-s -w\" -trimpath %s\n", target)
	cmd := exec.Command("go", "install", "-ldflags=-s -w", "-trimpath", target)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go install: %w", err)
	}
	fmt.Fprintf(os.Stdout, "sparkwing updated via go install -> %s\n", version)
	return nil
}

func runUpdateSDK(version string) error {
	dir, err := findSparkwingDir()
	if err != nil {
		return err
	}

	v := strings.TrimSpace(version)
	if v == "" {
		v = "latest"
	}
	target := "github.com/" + updateRepo + "@" + v
	fmt.Fprintf(os.Stdout, "bumping pipeline SDK to %s\n", v)

	cmd := exec.Command("go", "get", target)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go get: %w", err)
	}

	if gomod, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		for _, line := range strings.Split(string(gomod), "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, updateRepo) && !strings.HasPrefix(line, "module") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					fmt.Fprintf(os.Stdout, "SDK: %s\n", parts[len(parts)-1])
				}
			}
		}
	}

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	tidy.Stdout = os.Stdout
	tidy.Stderr = os.Stderr
	if err := tidy.Run(); err != nil {
		return fmt.Errorf("go mod tidy: %w", err)
	}
	fmt.Fprintln(os.Stdout, "done")
	return nil
}
