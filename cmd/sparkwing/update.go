// CLI self-update verbs. Mirrors install.sh: fetch the bare per-platform
// binary, verify a signed SHA256SUMS, then atomically install and re-hash
// the installed bytes. The release ad-hoc-codesigns the macOS Mach-O
// binaries BEFORE SHA256SUMS is computed, so the verified bytes are
// already Gatekeeper-valid and the updater installs them UNCHANGED --
// there is no post-verification mutation. Windows uses a rename-aside
// dance. Every failure is terminal: the updater never falls back to an
// unverified build.
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
	updateRepo      = "sparkwing-dev/sparkwing"
	updateAssetBase = "https://github.com/" + updateRepo + "/releases/download"
)

// updateBaseURL is the release-download base the updater fetches assets
// from. It is a package var only so tests can point it at a local httptest
// server; production always uses the GitHub releases base.
var updateBaseURL = updateAssetBase

// sparkwingUpdatePubKeyHex is the ed25519 public key (hex-encoded, 32
// bytes = 64 hex chars) the updater uses to verify the detached signature
// over a release's SHA256SUMS. Verification is pure-Go crypto/ed25519 --
// no external binary, and no network beyond fetching the asset, its
// SHA256SUMS, and SHA256SUMS.sig.
//
// PLACEHOLDER -- NOT A REAL KEY. The release owner MUST replace these
// all-zero bytes with the public key printed by
//
//	go run ./cmd/sign-manifest -genkey
//
// and store the matching private key as the SPARKWING_UPDATE_SIGNING_KEY
// GitHub Actions secret (see .github/workflows/release.yaml). Until then a
// real release's signature CANNOT verify against these zero bytes, so
// `sparkwing update` fails CLOSED -- it refuses to install rather than
// installing bytes it cannot prove are the release's. A half-armed release
// (signed manifest, but this key still the placeholder) therefore fails
// safe, never open.
const sparkwingUpdatePubKeyHex = "0000000000000000000000000000000000000000000000000000000000000000"

// Compile-time proof the embedded key is exactly 32 bytes (64 hex chars).
// Both are constant expressions that must stay non-negative; a wrong
// length makes one negative and the conversion to uint fails to compile.
const (
	_ = uint(len(sparkwingUpdatePubKeyHex) - 64)
	_ = uint(64 - len(sparkwingUpdatePubKeyHex))
)

// updateVerifyKey is the decoded verification key. It is a package var so
// tests can inject a known keypair via withTestUpdateKey; production code
// never reassigns it, and there is no flag or env override -- the trusted
// key is compiled in.
var updateVerifyKey = mustDecodeUpdateKey(sparkwingUpdatePubKeyHex)

// isPlaceholderUpdateKey reports whether k is the unarmed all-zero
// placeholder. The updater refuses to verify against it (fail closed).
func isPlaceholderUpdateKey(k ed25519.PublicKey) bool {
	if len(k) != ed25519.PublicKeySize {
		return true
	}
	for _, b := range k {
		if b != 0 {
			return false
		}
	}
	return true
}

func mustDecodeUpdateKey(h string) ed25519.PublicKey {
	b, err := hex.DecodeString(strings.TrimSpace(h))
	if err != nil || len(b) != ed25519.PublicKeySize {
		panic("sparkwing: embedded update public key is not 32 hex-encoded bytes")
	}
	return ed25519.PublicKey(b)
}

// afterInstallHook, when non-nil, runs immediately after the atomic swap
// and BEFORE the installed-bytes re-hash. It exists only as a test seam to
// corrupt the just-installed file and exercise the post-install
// verification + restore path. It is nil in production.
var afterInstallHook func(installedPath string)

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

// runUpdateBinary downloads, verifies (signature + digest), and atomically
// installs a release binary, then re-hashes the installed file and
// requires it to equal the verified release digest.
//
// Every failure is TERMINAL. There is no `go install` fallback: an
// unverifiable download must not be silently swapped for an unverified
// build that `go install` may drop in a different directory than the
// running binary. An operator version hold is enforced here (unless
// overrideHold): the ceiling binds every self-upgrade path, `sparkwing
// update` and `sparkwing version update --cli` alike.
func runUpdateBinary(version string, force, overrideHold bool) error {
	resolved := strings.TrimSpace(version)
	if resolved == "" {
		v, err := fetchLatestRelease()
		if err != nil {
			return fmt.Errorf("update: could not determine the latest release: %w\n"+
				"  self-update does not fall back to `go install`; retry, pass --version vX.Y.Z, "+
				"or reinstall out-of-band via bin/install.sh", err)
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

	res, err := downloadAndInstall(resolved, currentBin)
	if err != nil {
		// Terminal. The running binary is untouched on every pre-install
		// failure, and restored on a post-install mismatch (see
		// downloadAndInstall); either way we do NOT reach `go install`.
		return fmt.Errorf("update: verified install failed: %w", err)
	}

	// Contract point 6: name the installed path, version, and digest.
	fmt.Fprintf(os.Stdout, "sparkwing updated: %s -> %s\n", current, res.version)
	fmt.Fprintf(os.Stdout, "  path:   %s\n", res.path)
	fmt.Fprintf(os.Stdout, "  digest: %s (sha256, verified against the signed SHA256SUMS)\n", res.digest)
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

// installResult reports what downloadAndInstall proved and installed.
type installResult struct {
	path    string // absolute path the verified bytes now occupy
	version string // release tag installed
	digest  string // sha256 hex, verified against the signed SHA256SUMS
}

// downloadAndInstall fetches the asset + SHA256SUMS + SHA256SUMS.sig,
// verifies the ed25519 signature over SHA256SUMS with the embedded key,
// checks the downloaded file against the signed digest, stages the bytes
// UNCHANGED (no codesign, no mutation), atomically installs, then
// re-hashes the installed file and requires it to equal the verified
// digest -- restoring the prior binary and failing loudly on mismatch.
func downloadAndInstall(version, currentBin string) (installResult, error) {
	suffix := runtime.GOOS + "-" + runtime.GOARCH
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	asset := "sparkwing-" + suffix + ext
	base := updateBaseURL + "/" + version

	tmpDir, err := os.MkdirTemp("", "sparkwing-update-")
	if err != nil {
		return installResult{}, fmt.Errorf("mkdir tmp: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	binPath := filepath.Join(tmpDir, asset)
	if err := downloadFile(base+"/"+asset, binPath, maxAssetBytes); err != nil {
		return installResult{}, fmt.Errorf("download %s: %w", asset, err)
	}
	sumsPath := filepath.Join(tmpDir, "SHA256SUMS")
	if err := downloadFile(base+"/SHA256SUMS", sumsPath, maxMetaBytes); err != nil {
		return installResult{}, fmt.Errorf("download SHA256SUMS: %w", err)
	}
	sigPath := filepath.Join(tmpDir, "SHA256SUMS.sig")
	if err := downloadFile(base+"/SHA256SUMS.sig", sigPath, maxMetaBytes); err != nil {
		return installResult{}, fmt.Errorf("download SHA256SUMS.sig: %w\n"+
			"  this release is not signed for verified self-update; install it out-of-band via bin/install.sh instead", err)
	}

	// Verify the ed25519 signature over the RAW SHA256SUMS bytes with the
	// embedded key BEFORE trusting any digest the manifest lists. With the
	// placeholder key this fails for every real release (fail closed).
	sumsBytes, err := os.ReadFile(sumsPath)
	if err != nil {
		return installResult{}, err
	}
	sigBytes, err := os.ReadFile(sigPath)
	if err != nil {
		return installResult{}, err
	}
	// Fail closed if this build was never armed. The placeholder key is
	// all-zero -- a low-order ed25519 point some verifiers will accept a
	// crafted signature against -- so we refuse outright rather than trust
	// ed25519.Verify's behavior on it. A real armed build never trips this.
	if isPlaceholderUpdateKey(updateVerifyKey) {
		return installResult{}, errors.New(
			"this sparkwing build has no update signing key compiled in (placeholder); " +
				"verified self-update is not armed -- install releases via bin/install.sh")
	}
	if !ed25519.Verify(updateVerifyKey, sumsBytes, sigBytes) {
		return installResult{}, errors.New(
			"SHA256SUMS signature is not valid for the key compiled into this binary; " +
				"refusing to install (the running binary was not touched)")
	}

	// One data path: the digest comes from the SAME in-memory bytes the
	// signature was verified over -- never re-read from disk -- so nothing
	// can swap the manifest between verification and lookup.
	expected, err := lookupSHA256(sumsBytes, asset)
	if err != nil {
		return installResult{}, err
	}
	actual, err := sha256OfFile(binPath)
	if err != nil {
		return installResult{}, err
	}
	if !strings.EqualFold(expected, actual) {
		return installResult{}, fmt.Errorf(
			"checksum mismatch for %s (download corrupt or tampered); refusing to install\n  expected: %s\n  actual:   %s",
			asset, expected, actual)
	}

	stagedBin := currentBin + ".update.tmp"
	if err := copyFile(binPath, stagedBin); err != nil {
		return installResult{}, fmt.Errorf("stage new binary: %w", err)
	}
	if err := os.Chmod(stagedBin, 0o755); err != nil {
		_ = os.Remove(stagedBin)
		return installResult{}, err
	}
	// NO post-verification byte mutation. The macOS ad-hoc codesign moved
	// to the release (rcodesign on the Linux runner, before SHA256SUMS is
	// computed), so the verified bytes are already Gatekeeper-valid and
	// install exactly as hashed.

	prev, err := installAtomic(stagedBin, currentBin)
	if err != nil {
		_ = os.Remove(stagedBin)
		return installResult{}, fmt.Errorf("install: %w", err)
	}

	// Test seam: corrupt the just-installed file to exercise the recheck.
	if afterInstallHook != nil {
		afterInstallHook(currentBin)
	}

	// Contract point 4: hash the INSTALLED file and require equality with
	// the verified release digest. A mismatch here means the bytes on disk
	// are not the release's bytes -- restore the prior binary and fail.
	installed, herr := sha256OfFile(currentBin)
	if herr != nil || !strings.EqualFold(installed, expected) {
		if rerr := restorePrev(prev, currentBin); rerr != nil {
			return installResult{}, fmt.Errorf(
				"SECURITY: installed bytes do not match the verified release digest AND restoring the prior binary FAILED\n"+
					"  expected:  %s\n  installed: %s\n  prior binary preserved at: %s\n  restore error: %v\n"+
					"  do NOT run sparkwing until you replace it with a known-good binary (bin/install.sh)",
				expected, installedOr(installed, herr), prev, rerr)
		}
		return installResult{}, fmt.Errorf(
			"SECURITY: installed bytes do not match the verified release digest; restored the prior binary, no change made\n"+
				"  expected:  %s\n  installed: %s",
			expected, installedOr(installed, herr))
	}

	// Success: drop the preserved prior binary. On Windows it is the still
	// running .old image and cannot be deleted now; cleanupStaleUpdate
	// removes it at next launch, so ignore the error there.
	_ = os.Remove(prev)

	return installResult{path: currentBin, version: version, digest: expected}, nil
}

// installedOr renders the installed-file hash, or a reason it could not be
// read, for a post-install mismatch message.
func installedOr(hash string, err error) string {
	if err != nil {
		return "(could not read installed file: " + err.Error() + ")"
	}
	return hash
}

// installAtomic swaps stagedBin in for currentBin and preserves the prior
// binary so a caller that detects a post-install problem can restore it.
// It returns the path where the prior binary is preserved.
//
//   - non-Windows: the prior bytes are copied aside to <current>.prev, then
//     the staged binary is renamed over the running one in a single atomic
//     rename (the running process keeps its open inode).
//   - Windows: the running .exe cannot be overwritten, so it is renamed
//     aside to <current>.old (which becomes the preserved prior) and the
//     staged binary is renamed into place. cleanupStaleUpdate deletes the
//     .old at next launch.
func installAtomic(stagedBin, currentBin string) (prev string, err error) {
	if runtime.GOOS == "windows" {
		// Windows cannot overwrite or delete a running .exe, so this is two
		// renames rather than one. Between them there is a brief window
		// where no binary sits at currentBin; a crash there leaves the new
		// binary at currentBin (second rename) or the prior one recoverable
		// at oldBin, and cleanupStaleUpdate reconciles .old at next launch.
		// POSIX below is a single atomic rename with no such window.
		oldBin := currentBin + ".old"
		_ = os.Remove(oldBin)
		if err := os.Rename(currentBin, oldBin); err != nil {
			return "", fmt.Errorf("move running binary aside: %w", err)
		}
		if err := os.Rename(stagedBin, currentBin); err != nil {
			_ = os.Rename(oldBin, currentBin) // roll back the aside move
			return "", fmt.Errorf("install new binary: %w", err)
		}
		return oldBin, nil
	}

	prev = currentBin + ".prev"
	_ = os.Remove(prev)
	if err := copyFile(currentBin, prev); err != nil {
		return "", fmt.Errorf("back up current binary: %w", err)
	}
	_ = os.Chmod(prev, 0o755)
	if err := os.Rename(stagedBin, currentBin); err != nil {
		_ = os.Remove(prev)
		return "", fmt.Errorf("install new binary: %w", err)
	}
	return prev, nil
}

// restorePrev puts the preserved prior binary back at currentBin after a
// failed post-install check. On Windows the freshly-installed (non-running)
// binary is removed first so the preserved .old can be renamed back.
func restorePrev(prev, currentBin string) error {
	if prev == "" {
		return errors.New("no preserved prior binary to restore")
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(currentBin)
		return os.Rename(prev, currentBin)
	}
	return os.Rename(prev, currentBin)
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

// Download size ceilings. Bodies are bounded BEFORE verification so a
// hostile or broken endpoint cannot stream an unbounded response into
// memory/disk; an over-limit body is a terminal download error.
const (
	maxAssetBytes = 512 << 20 // 512 MiB -- generous for the binary asset
	maxMetaBytes  = 1 << 20   // 1 MiB -- tight for SHA256SUMS and .sig
)

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
	// Read one byte past the ceiling so an exactly-at-limit body still
	// succeeds while anything larger is caught and rejected.
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if n > maxBytes {
		return fmt.Errorf("response body exceeds %d bytes; refusing", maxBytes)
	}
	return nil
}

// lookupSHA256 extracts the digest for filename from already-verified
// SHA256SUMS bytes. It deliberately takes bytes, not a path, so the digest
// is read from the same content the signature covered (no disk re-read).
func lookupSHA256(sums []byte, filename string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
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
