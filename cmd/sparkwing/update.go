// CLI self-update verbs. Mirrors install.sh: fetch the bare per-platform
// binary, verify SHA256, atomic-rename onto the running binary. macOS
// gets ad-hoc codesigning; Windows uses a rename-aside dance.
package main

import (
	"crypto/ed25519"
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

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
)

const (
	updateRepo             = "sparkwing-dev/sparkwing"
	defaultUpdateAssetBase = "https://github.com/" + updateRepo + "/releases/download"
	maxAssetBytes          = 512 << 20
	maxMetadataBytes       = 1 << 20
)

var (
	updateFetchLatest     = fetchLatestRelease
	updateDownloadInstall = downloadAndInstall
	updateBaseURL         = defaultUpdateAssetBase
	updateVerifyKey       ed25519.PublicKey
)

// runUpdate is the top-level binary self-update verb (CLI only; for
// SDK pins, see `sparkwing version update --sdk`).
func runUpdate(args []string) error {
	fs := flag.NewFlagSet(cmdUpdate.Path, flag.ContinueOnError)
	check := fs.Bool("check", false, "report current vs latest; exit 1 if a newer release exists")
	force := fs.Bool("force", false, "allow downgrading to an older release")
	version := fs.String("version", "", "target release tag (e.g. v0.17.0). Default: latest.")
	overrideHold := fs.Bool("override-hold", false, "cross an operator version hold (do not use to defy an operator)")
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
	return runUpdateBinary(*version, *force, *overrideHold)
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

// downgradeKind classifies a move from the installed version to a
// resolved target.
type downgradeKind int

const (
	// downgradeAllowed: the target is the same or newer, or the versions
	// aren't comparable, so the move proceeds unremarked.
	downgradeAllowed downgradeKind = iota
	// downgradeRebaseline: the target sorts older, but the installed
	// build is unpublished (a pseudo-version or dirty worktree) and so is
	// not a release worth protecting. Moving to the published target is a
	// re-baseline, not a downgrade, and proceeds without --force.
	downgradeRebaseline
	// downgradeNeedsForce: both versions are resolvable published
	// releases and the target is older, so the move is a real downgrade
	// gated behind --force.
	downgradeNeedsForce
)

// classifyDowngrade decides how a move from current to resolved is
// treated. The downgrade guard protects only between two published
// releases: an unpublished build (pseudo-version such as
// v1.6.2-0.<ts>-<hash>, or a +dirty worktree) that merely sorts above
// the published latest must still re-baseline to it, not be mistaken
// for a newer release the guard should defend.
func classifyDowngrade(current, resolved string) downgradeKind {
	if !isSemver(current) || !isSemver(resolved) || semver.Compare(resolved, current) >= 0 {
		return downgradeAllowed
	}
	if isResolvableModuleVersion(current) {
		return downgradeNeedsForce
	}
	return downgradeRebaseline
}

// runUpdateBinary downloads, authenticates, and installs a release. An operator
// version hold is enforced here (unless overrideHold): the ceiling
// binds every self-upgrade path, `sparkwing update` and
// `sparkwing version update --cli` alike.
func runUpdateBinary(version string, force, overrideHold bool) error {
	resolved := strings.TrimSpace(version)
	if resolved == "" {
		v, err := updateFetchLatest()
		if err != nil {
			return fmt.Errorf("update: fetch latest version: %w", err)
		}
		resolved = v
	}

	current := installedVersion()

	if current != "(unknown)" && current != "(devel)" && resolved == current {
		fmt.Fprintf(os.Stdout, "sparkwing is already at %s\n", current)
		return nil
	}

	if hold := resolveVersionHold(); hold.Value != "" && exceedsHold(resolved, hold.Value) {
		if !overrideHold {
			return holdRefusal(resolved, hold)
		}
		fmt.Fprintf(os.Stderr, "update: crossing operator version hold %s (%s) via --override-hold\n",
			hold.Value, hold.Source)
	}

	switch classifyDowngrade(current, resolved) {
	case downgradeNeedsForce:
		if !force {
			return fmt.Errorf(
				"update: %s is older than the installed %s\n  to downgrade, re-run with --force",
				resolved, current,
			)
		}
	case downgradeRebaseline:
		fmt.Fprintf(os.Stdout,
			"update: installed %s is an unpublished build; re-baselining to the published %s\n",
			current, resolved,
		)
	}

	currentBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	currentBin, _ = filepath.EvalSymlinks(currentBin)

	fmt.Fprintf(os.Stdout, "updating sparkwing: %s -> %s\n", current, resolved)

	result, err := updateDownloadInstall(resolved, currentBin)
	if err != nil {
		return fmt.Errorf("update: verified release install failed: %w", err)
	}

	fmt.Fprintf(os.Stdout, "sparkwing updated: %s -> %s\ninstalled: %s\nsha256: %s\n", current, resolved, result.path, result.digest)
	reportOtherInstalls(os.Stdout, currentBin)
	fmt.Fprintf(os.Stdout, "what's new: https://github.com/sparkwing-dev/sparkwing/releases\n")
	return nil
}

// reportOtherInstalls names every other sparkwing binary reachable on
// the machine after an install landed at installedBin, with the exact
// remedy per copy. It is strictly read-only: renaming a binary
// elsewhere on the user's machine is a bigger action than update was
// asked for, and the operator may well have installed the other copy
// on purpose. Nothing here is an error -- the install itself succeeded,
// and failing after it would send the caller looking for a broken
// binary that is fine.
func reportOtherInstalls(w io.Writer, installedBin string) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	writeOtherInstallsNote(w, installedBin,
		installsite.Competing(installsite.Scan(installsite.SearchDirs(os.Getenv, home)), installedBin))
}

func writeOtherInstallsNote(w io.Writer, installedBin string, others []installsite.Copy) {
	if len(others) == 0 {
		return
	}
	noun := "binaries are"
	if len(others) == 1 {
		noun = "binary is"
	}
	fmt.Fprintf(w, "\nnote: %d other sparkwing %s installed on this machine:\n", len(others), noun)
	for _, c := range others {
		fmt.Fprintf(w, "  %s (modified %s) -- retire it with: %s\n",
			c.Path, c.ModTime.Format("2006-01-02 15:04"), installsite.RetireRemedy(c.Path).Text())
	}
	fmt.Fprintf(w, "  a shell and a background job can resolve different copies of `sparkwing`, so the same command can be two builds.\n")
	fmt.Fprintf(w, "  keep one, or point each job at %s by absolute path. `sparkwing doctor` shows the full picture.\n", installedBin)
}

// runVersionUpdate dispatches `sparkwing version update`. Requires
// exactly one of --cli or --sdk so it can't silently flip the wrong half.
func runVersionUpdate(args []string) error {
	fs := flag.NewFlagSet(cmdVersionUpdate.Path, flag.ContinueOnError)
	cli := fs.Bool("cli", false, "self-update the sparkwing CLI binary")
	sdk := fs.Bool("sdk", false, "bump the SDK pin in this project's .sparkwing/go.mod")
	version := fs.String("version", "", "target release (e.g. v0.17.0). Default: latest.")
	force := fs.Bool("force", false, "allow downgrading to an older release")
	overrideHold := fs.Bool("override-hold", false, "cross an operator version hold (do not use to defy an operator)")
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
		return runUpdateBinary(*version, *force, *overrideHold)
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

type installedRelease struct {
	path    string
	version string
	digest  string
}

// downloadAndInstall authenticates the manifest and platform asset before installation.
func downloadAndInstall(version, currentBin string) (installedRelease, error) {
	suffix := runtime.GOOS + "-" + runtime.GOARCH
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	asset := "sparkwing-" + suffix + ext
	base := updateBaseURL + "/" + version

	tmpDir, err := os.MkdirTemp("", "sparkwing-update-")
	if err != nil {
		return installedRelease{}, fmt.Errorf("mkdir tmp: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	binPath := filepath.Join(tmpDir, asset)
	if err := downloadFile(base+"/"+asset, binPath, maxAssetBytes); err != nil {
		return installedRelease{}, fmt.Errorf("download %s: %w", asset, err)
	}
	assetSigPath := binPath + ".sig"
	if err := downloadFile(base+"/"+asset+".sig", assetSigPath, maxMetadataBytes); err != nil {
		return installedRelease{}, fmt.Errorf("download %s.sig: %w", asset, err)
	}
	sumsPath := filepath.Join(tmpDir, "SHA256SUMS")
	if err := downloadFile(base+"/SHA256SUMS", sumsPath, maxMetadataBytes); err != nil {
		return installedRelease{}, fmt.Errorf("download SHA256SUMS: %w", err)
	}
	sumsSigPath := sumsPath + ".sig"
	if err := downloadFile(base+"/SHA256SUMS.sig", sumsSigPath, maxMetadataBytes); err != nil {
		return installedRelease{}, fmt.Errorf("download SHA256SUMS.sig: %w", err)
	}
	assetBody, err := os.ReadFile(binPath)
	if err != nil {
		return installedRelease{}, err
	}
	assetSig, err := os.ReadFile(assetSigPath)
	if err != nil {
		return installedRelease{}, err
	}
	manifest, err := os.ReadFile(sumsPath)
	if err != nil {
		return installedRelease{}, err
	}
	manifestSig, err := os.ReadFile(sumsSigPath)
	if err != nil {
		return installedRelease{}, err
	}
	publicKeys, err := releasePublicKeys()
	if err != nil {
		return installedRelease{}, err
	}
	verified, err := verifyReleaseAssetWithTrustSet(publicKeys, manifest, manifestSig, asset, assetBody, assetSig)
	if err != nil {
		return installedRelease{}, err
	}
	if err := installVerifiedAsset(verified, currentBin); err != nil {
		return installedRelease{}, err
	}
	return installedRelease{path: currentBin, version: version, digest: verified.digest}, nil
}

// cleanupStaleUpdate removes residue left by pre-integrity Windows updaters.
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

func downloadFile(url, dst string, limits ...int64) error {
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
	var reader io.Reader = resp.Body
	if len(limits) > 0 {
		reader = io.LimitReader(resp.Body, limits[0]+1)
	}
	written, err := io.Copy(f, reader)
	if err != nil {
		return err
	}
	if len(limits) > 0 && written > limits[0] {
		return fmt.Errorf("download exceeds %d-byte limit", limits[0])
	}
	return nil
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
