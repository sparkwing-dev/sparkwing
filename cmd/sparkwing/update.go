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
	"time"

	flag "github.com/spf13/pflag"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
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

func runUpdate(args []string) error {
	fs := flag.NewFlagSet(cmdUpdate.Path, flag.ContinueOnError)
	cli := fs.Bool("cli", false, "update the CLI binary (default target)")
	sdk := fs.Bool("sdk", false, "update this project's SDK pin")
	check := fs.Bool("check", false, "compare the selected target without changing it")
	force := fs.Bool("force", false, "allow CLI downgrades")
	version := fs.String("version", "", "target release tag; omit for latest published release")
	overrideHold := fs.Bool("override-hold", false, "cross an operator CLI version hold")
	output := fs.StringP("output", "o", "", "pretty | json | plain")
	if err := parseAndCheck(cmdUpdate, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("update: unexpected positional %q", fs.Arg(0))
	}
	if fs.Changed("cli") && fs.Changed("sdk") {
		return errors.New("update: --cli and --sdk are mutually exclusive")
	}
	if *sdk && (fs.Changed("force") || fs.Changed("override-hold")) {
		return errors.New("update: --force and --override-hold apply only to --cli")
	}
	if fs.Changed("cli") && !*cli || fs.Changed("sdk") && !*sdk {
		return errors.New("update: target selectors must be enabled; choose --cli or --sdk")
	}
	if fs.Changed("version") && !validUpdateVersion(*version) {
		return errors.New("update: --version must be a canonical release tag such as v0.48.1; omit it for latest")
	}
	mode, err := resolveOutputFormat(*output, cmdUpdate.Path)
	if err != nil {
		return err
	}
	target := "cli"
	if *sdk {
		target = "sdk"
	}
	if *check {
		return runUpdateCheck(target, *version, *force, *overrideHold, mode)
	}
	var result updateReceipt
	if *sdk {
		result, err = updateSDK(*version)
	} else {
		result, err = updateBinary(*version, *force, *overrideHold)
	}
	if err != nil {
		return err
	}
	return writeUpdateReceipt(os.Stdout, result, mode)
}

func validUpdateVersion(version string) bool {
	return semver.IsValid(version) && semver.Canonical(version) == version
}

type downgradeKind int

const (
	downgradeAllowed downgradeKind = iota

	downgradeRebaseline

	downgradeNeedsForce
)

func classifyDowngrade(current, resolved string) downgradeKind {
	if !isSemver(current) || !isSemver(resolved) || semver.Compare(resolved, current) >= 0 {
		return downgradeAllowed
	}
	if isResolvableModuleVersion(current) {
		return downgradeNeedsForce
	}
	return downgradeRebaseline
}

func runUpdateBinary(version string, force, overrideHold bool) error {
	_, err := updateBinary(version, force, overrideHold)
	return err
}

func updateBinary(version string, force, overrideHold bool) (updateReceipt, error) {
	result := updateReceipt{Kind: "update", Tool: "sparkwing", Target: "cli", Strategy: "release"}
	resolved, err := resolveUpdateVersion(version, false)
	if err != nil {
		return result, err
	}
	identity := updateReadInstalled()
	current := identity.Version

	currentBin, err := os.Executable()
	if err != nil {
		return result, fmt.Errorf("locate current binary: %w", err)
	}
	currentBin, err = filepath.EvalSymlinks(currentBin)
	if err != nil {
		return result, fmt.Errorf("resolve current binary: %w", err)
	}
	identity.Path = currentBin
	result.Before = identity
	verified, local, provenanceReason := installedReleaseProvenance(identity)
	if resolved == current && verified {
		result.Status = "current"
		result.After = result.Before
		return result, nil
	}
	if !verified {
		fmt.Fprintf(os.Stderr, "update: %s; replacement will use verified release assets\n", provenanceReason)
	}
	hold := resolveVersionHold()
	if hold.Error != "" {
		return result, fmt.Errorf("update refused: %s", hold.Error)
	}
	if hold.Value != "" && exceedsHold(resolved, hold.Value) {
		if !overrideHold {
			return result, holdRefusal(resolved, hold)
		}
		fmt.Fprintf(os.Stderr, "update: crossing operator version hold %s (%s) via --override-hold\n", hold.Value, hold.Source)
	}
	downgrade := classifyDowngrade(current, resolved)
	if local && downgrade == downgradeNeedsForce {
		downgrade = downgradeRebaseline
	}
	switch downgrade {
	case downgradeNeedsForce:
		if !force {
			return result, fmt.Errorf("update: %s is older than the installed %s; re-run with --force to downgrade", resolved, current)
		}
	case downgradeRebaseline:
		fmt.Fprintf(os.Stderr, "update: installed %s is an unpublished build; re-baselining to the published %s\n", current, resolved)
	}
	fmt.Fprintf(os.Stderr, "updating sparkwing: %s -> %s\n", current, resolved)
	installed, err := updateDownloadInstall(resolved, currentBin)
	if err != nil {
		return result, fmt.Errorf("update: verified release install failed: %w", err)
	}
	result.Status = "updated"
	result.After = updateIdentity{Version: installed.version, Path: installed.path}
	result.Digest = installed.digest
	reportOtherInstalls(os.Stderr, currentBin)
	return result, nil
}

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

func installedVersion() string {
	return buildinfo.Read("sparkwing", Version).Version
}

var Version string

type installedRelease struct {
	path    string
	version string
	digest  string
}

func downloadAndInstall(version, currentBin string) (installedRelease, error) {
	verified, err := fetchVerifiedRelease(version)
	if err != nil {
		return installedRelease{}, err
	}
	if err := installVerifiedAsset(verified, currentBin); err != nil {
		return installedRelease{}, err
	}
	return installedRelease{path: currentBin, version: version, digest: verified.digest}, nil
}

func releaseAssetName() string {
	name, err := currentReleaseTarget(releaseasset.Sparkwing).Name()
	if err != nil {
		panic(err)
	}
	return name
}

func currentReleaseTarget(binary releaseasset.Binary) releaseasset.Target {
	return releaseasset.Target{Binary: binary, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
}

func releaseBaseURL(version string) string { return updateBaseURL + "/" + version }

func fetchVerifiedRelease(version string) (verifiedReleaseAsset, error) {
	return fetchVerifiedReleaseTarget(version, currentReleaseTarget(releaseasset.Sparkwing))
}

func fetchVerifiedReleaseTarget(version string, target releaseasset.Target) (verifiedReleaseAsset, error) {
	asset, err := target.Name()
	if err != nil {
		return verifiedReleaseAsset{}, err
	}
	base := releaseBaseURL(version)

	tmpDir, err := os.MkdirTemp("", "sparkwing-update-")
	if err != nil {
		return verifiedReleaseAsset{}, fmt.Errorf("mkdir tmp: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	binPath := filepath.Join(tmpDir, asset)
	if err := downloadFile(base+"/"+asset, binPath, maxAssetBytes); err != nil {
		return verifiedReleaseAsset{}, fmt.Errorf("download %s: %w", asset, err)
	}
	assetSigPath := binPath + ".sig"
	if err := downloadFile(base+"/"+asset+".sig", assetSigPath, maxMetadataBytes); err != nil {
		return verifiedReleaseAsset{}, fmt.Errorf("download %s.sig: %w", asset, err)
	}
	sumsPath := filepath.Join(tmpDir, "SHA256SUMS")
	if err := downloadFile(base+"/SHA256SUMS", sumsPath, maxMetadataBytes); err != nil {
		return verifiedReleaseAsset{}, fmt.Errorf("download SHA256SUMS: %w", err)
	}
	sumsSigPath := sumsPath + ".sig"
	if err := downloadFile(base+"/SHA256SUMS.sig", sumsSigPath, maxMetadataBytes); err != nil {
		return verifiedReleaseAsset{}, fmt.Errorf("download SHA256SUMS.sig: %w", err)
	}
	assetBody, err := os.ReadFile(binPath)
	if err != nil {
		return verifiedReleaseAsset{}, err
	}
	assetSig, err := os.ReadFile(assetSigPath)
	if err != nil {
		return verifiedReleaseAsset{}, err
	}
	manifest, err := os.ReadFile(sumsPath)
	if err != nil {
		return verifiedReleaseAsset{}, err
	}
	manifestSig, err := os.ReadFile(sumsSigPath)
	if err != nil {
		return verifiedReleaseAsset{}, err
	}
	publicKeys, err := releasePublicKeys()
	if err != nil {
		return verifiedReleaseAsset{}, err
	}
	verified, err := releaseasset.Verify(publicKeys, manifest, manifestSig, target, assetBody, assetSig)
	if err != nil {
		return verifiedReleaseAsset{}, err
	}
	return fromSharedVerified(verified), nil
}

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

func downloadFile(url, dst string, maxBytes int64) error {
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
	written, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if written > maxBytes {
		return fmt.Errorf("download exceeds %d-byte limit", maxBytes)
	}
	return nil
}

func sha256OfFile(path string) (string, error) {
	// #nosec G703 -- a file the updater downloaded into its own staging directory
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

func updateSDK(version string) (updateReceipt, error) {
	result := updateReceipt{Kind: "update", Tool: "sparkwing", Target: "sdk", Strategy: "release"}
	dir, err := findSparkwingDir()
	if err != nil {
		return result, err
	}
	before, err := readSDKUpdateIdentity(dir)
	if err != nil {
		return result, err
	}
	result.Before = before.Identity
	resolved, err := resolveUpdateVersion(version, false)
	if err != nil {
		return result, err
	}
	target := sdkModulePath + "@" + resolved
	fmt.Fprintf(os.Stderr, "bumping pipeline SDK to %s\n", resolved)
	cmd := exec.Command("go", "get", target)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return result, fmt.Errorf("go get failed; SDK files may have changed: %w", err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	tidy.Stdout = os.Stderr
	tidy.Stderr = os.Stderr
	if err := tidy.Run(); err != nil {
		return result, fmt.Errorf("go mod tidy failed after go get; SDK files may have changed: %w", err)
	}
	after, err := readSDKUpdateIdentity(dir)
	if err != nil {
		return result, fmt.Errorf("read updated SDK pin: %w", err)
	}
	result.After = after.Identity
	result.Replacement = after.Replacement
	result.Status = "updated"
	if before.Identity.Version == after.Identity.Version {
		result.Status = "current"
	}
	return result, nil
}
