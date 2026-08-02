// Command installguard serializes source-built Sparkwing CLI installation and
// refuses to replace a machine-global binary with an older source revision.
package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/module"
)

const (
	defaultLockTimeout = 2 * time.Minute
)

type buildIdentity struct {
	Version  string    `json:"version"`
	Revision string    `json:"revision"`
	Time     time.Time `json:"time"`
	Modified bool      `json:"modified"`
	SHA256   string    `json:"sha256"`
}

func (i buildIdentity) String() string {
	parts := []string{i.Version}
	if i.Revision != "" {
		rev := i.Revision
		if len(rev) > 12 {
			rev = rev[:12]
		}
		parts = append(parts, rev)
	}
	if !i.Time.IsZero() {
		parts = append(parts, i.Time.UTC().Format(time.RFC3339))
	}
	if i.Modified {
		parts = append(parts, "dirty")
	}
	return strings.Join(parts, " ")
}

func readBuildIdentity(path string) (buildIdentity, error) {
	if body, err := os.ReadFile(path + ".build.json"); err == nil {
		var id buildIdentity
		if err := json.Unmarshal(body, &id); err == nil && !id.Time.IsZero() && id.SHA256 != "" {
			if actual, hashErr := fileSHA256(path); hashErr == nil && actual == id.SHA256 {
				return id, nil
			}
		}
	}
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return buildIdentity{}, err
	}
	id := buildIdentity{Version: bi.Main.Version}
	for _, setting := range bi.Settings {
		switch setting.Key {
		case "vcs.revision":
			id.Revision = setting.Value
		case "vcs.time":
			id.Time, _ = time.Parse(time.RFC3339, setting.Value)
		case "vcs.modified":
			id.Modified, _ = strconv.ParseBool(setting.Value)
		}
	}
	if id.Time.IsZero() && module.IsPseudoVersion(id.Version) {
		id.Time, _ = module.PseudoVersionTime(id.Version)
	}
	if id.Time.IsZero() {
		return buildIdentity{}, fmt.Errorf("%s has no trustworthy VCS time", path)
	}
	return id, nil
}

type installDecision int

const (
	installCandidate installDecision = iota
	keepCurrent
	unorderedBuilds
)

func decideInstall(candidate, current buildIdentity, allowDowngrade bool) installDecision {
	if allowDowngrade {
		return installCandidate
	}
	if candidate.Time.After(current.Time) {
		return installCandidate
	}
	if candidate.Time.Before(current.Time) {
		return keepCurrent
	}
	if candidate.Revision != "" && candidate.Revision == current.Revision {
		// safety: a dirty build at the same commit is the explicit local-development
		// case. Installing it is useful; all other same-revision replacements
		// are redundant or would erase uncommitted behavior.
		if candidate.Modified && !current.Modified {
			return installCandidate
		}
		return keepCurrent
	}
	// safety: equal timestamps with different or missing revisions are unordered.
	// Fail closed rather than deciding from a semver label or invocation order.
	return unorderedBuilds
}

type identityReader func(string) (buildIdentity, error)

func installLocked(candidate, target string, candidateID buildIdentity, allowDowngrade bool, read identityReader) (bool, error) {
	if _, err := buildinfo.ReadFile(candidate); err != nil {
		return false, fmt.Errorf("inspect candidate: %w", err)
	}
	candidateHash, err := fileSHA256(candidate)
	if err != nil {
		return false, fmt.Errorf("hash candidate: %w", err)
	}
	candidateID.SHA256 = candidateHash
	currentID, err := read(target)
	if err == nil {
		switch decideInstall(candidateID, currentID, allowDowngrade) {
		case keepCurrent:
			fmt.Printf("kept newer sparkwing: %s\n", currentID)
			fmt.Printf("skipped older candidate: %s\n", candidateID)
			return false, nil
		case unorderedBuilds:
			return false, fmt.Errorf(
				"candidate %s and current binary %s are unordered; refusing replacement (set SPARKWING_INSTALL_ALLOW_DOWNGRADE=1 to recover explicitly)",
				candidateID, currentID,
			)
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		if !allowDowngrade {
			return false, fmt.Errorf(
				"current binary identity unavailable: %w; refusing an unordered replacement (set SPARKWING_INSTALL_ALLOW_DOWNGRADE=1 to recover explicitly)",
				err,
			)
		}
		fmt.Fprintf(os.Stderr, "install: current binary identity unavailable (%v); replacing via explicit downgrade override\n", err)
	}
	if err := os.Rename(candidate, target); err != nil {
		return false, fmt.Errorf("atomic install: %w", err)
	}
	if err := writeBuildIdentity(target, candidateID); err != nil {
		return false, fmt.Errorf("persist installed identity: %w", err)
	}
	fmt.Printf("installed sparkwing: %s\n", candidateID)
	return true, nil
}

func fileSHA256(path string) (string, error) {
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

func writeBuildIdentity(target string, id buildIdentity) error {
	body, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	tmp := fmt.Sprintf("%s.build.json.tmp.%d", target, os.Getpid())
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, target+".build.json"); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func withInstallLock(target string, timeout time.Duration, fn func() error) error {
	lockPath := target + ".install.lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open install lock: %w", err)
	}
	defer lockFile.Close()

	deadline := time.Now().Add(timeout)
	for {
		locked, err := tryInstallLock(lockFile)
		if err != nil {
			return fmt.Errorf("acquire install lock: %w", err)
		}
		if locked {
			defer func() { _ = unlockInstallLock(lockFile) }()
			if err := writeLockOwner(lockFile); err != nil {
				return fmt.Errorf("record install lock owner: %w", err)
			}
			return fn()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for install lock %s", lockPath)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func writeLockOwner(lockFile *os.File) error {
	if err := lockFile.Truncate(0); err != nil {
		return err
	}
	if _, err := lockFile.Seek(0, 0); err != nil {
		return err
	}
	_, err := fmt.Fprintf(lockFile, "pid=%d\n", os.Getpid())
	return err
}

func main() {
	var candidate, target, candidateVersion, candidateRevision, candidateTime string
	var allowDowngrade, candidateModified bool
	flag.StringVar(&candidate, "candidate", "", "source-built candidate binary")
	flag.StringVar(&target, "target", "", "machine-global binary path")
	flag.StringVar(&candidateVersion, "candidate-version", "", "candidate display version")
	flag.StringVar(&candidateRevision, "candidate-revision", "", "candidate Git revision")
	flag.StringVar(&candidateTime, "candidate-time", "", "candidate commit time (RFC3339)")
	flag.BoolVar(&candidateModified, "candidate-modified", false, "candidate was built from a dirty worktree")
	flag.BoolVar(&allowDowngrade, "allow-downgrade", false, "explicitly replace a newer installed build")
	flag.Parse()
	parsedTime, err := time.Parse(time.RFC3339, candidateTime)
	if candidate == "" || target == "" || candidateRevision == "" || err != nil {
		fmt.Fprintln(os.Stderr, "installguard: --candidate, --target, --candidate-revision, and RFC3339 --candidate-time are required")
		os.Exit(2)
	}
	candidateID := buildIdentity{
		Version: candidateVersion, Revision: candidateRevision,
		Time: parsedTime, Modified: candidateModified,
	}
	if err := withInstallLock(target, defaultLockTimeout, func() error {
		_, err := installLocked(candidate, target, candidateID, allowDowngrade, readBuildIdentity)
		return err
	}); err != nil {
		fmt.Fprintln(os.Stderr, "installguard:", err)
		os.Exit(1)
	}
}
