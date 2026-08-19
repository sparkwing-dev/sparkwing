// Package configguard fingerprints the sparkwing configuration that
// lives under the real user's home directory, so a test can assert the
// suite left it untouched.
//
// Every path here is resolved from $HOME on purpose. The rest of the
// codebase honors SPARKWING_REPOS, SPARKWING_HOME and XDG_CONFIG_HOME
// so a test can redirect itself somewhere disposable; this package must
// not, because the thing it exists to watch is the file those overrides
// are supposed to protect.
package configguard

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// WatchedFiles are the paths under the real user's home that sparkwing
// treats as configuration. A test binary has no business changing any of
// them.
//
// repos.yaml leads the list because it is the laptop's repository registry,
// and a single malformed line
// makes every fleet-wide command see an empty fleet.
//
// The state files under ~/.sparkwing -- last-version.d, outbox.db, state.db
// -- are deliberately absent. They belong to whatever sparkwing is
// running on the machine, and on a developer laptop that is a wingd
// daemon and a pre-commit pipeline on every commit. Asserting they are
// byte-identical across a suite run measures the neighbors, not the
// suite. SandboxLeaks covers them soundly instead.
func WatchedFiles(home string) []string {
	return []string{
		filepath.Join(home, ".config", "sparkwing", "repos.yaml"),
		filepath.Join(home, ".config", "sparkwing", "profiles.yaml"),
	}
}

// SandboxLeaks returns the sparkwing-owned paths that appeared under a
// stand-in home directory, which is how the audit detects a live-config
// write without racing anything else on the machine: run the suite with
// HOME pointed at an empty directory nobody else knows about, and
// anything sparkwing-shaped that shows up there is a path that would
// have been the developer's real home.
//
// This catches what a before/after comparison of the real home cannot: a
// test that spawns the compiled sparkwing binary writes as a normal
// process, so no in-process guard applies to it, and a machine with a
// daemon running makes the timestamps unreadable.
func SandboxLeaks(sandboxHome string) ([]string, error) {
	var leaks []string
	for _, root := range []string{
		filepath.Join(sandboxHome, ".config", "sparkwing"),
		filepath.Join(sandboxHome, ".sparkwing"),
	} {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() {
				leaks = append(leaks, p)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", root, err)
		}
	}
	sort.Strings(leaks)
	return leaks, nil
}

// Fingerprint records content hash, size and modification time for
// every watched file under home. A file that does not exist maps to the
// sentinel "<absent>" rather than being skipped, so a test binary that
// creates one on a machine that had none is caught the same way as one
// that rewrites an existing file.
//
// Modification time is in the fingerprint because content alone hides
// the failure this package was written for. The stamp writer under
// ~/.sparkwing rewrites the same bytes it read whenever the version has
// not moved, so a hash-only check calls a live-config write clean on the
// machine that happens to be at the stamped version, and only fails on
// the machine that is not. Touching the file at all is the bug.
func Fingerprint(home string) (map[string]string, error) {
	out := make(map[string]string, len(WatchedFiles(home)))
	for _, p := range WatchedFiles(home) {
		sum, err := fingerprintFile(p)
		if err != nil {
			return nil, err
		}
		out[p] = sum
	}
	return out, nil
}

// Diff reports the watched paths whose fingerprint changed between two
// snapshots, sorted so the failure message is stable.
func Diff(before, after map[string]string) []string {
	var changed []string
	for p, b := range before {
		if a, ok := after[p]; !ok || a != b {
			changed = append(changed, fmt.Sprintf("%s: %s -> %s", p, b, after[p]))
		}
	}
	sort.Strings(changed)
	return changed
}

// fingerprintFile returns "sha256=..,size=..,mtime=.." for path, or the
// sentinel "<absent>" when the file is not there.
func fingerprintFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "<absent>", nil
		}
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return fmt.Sprintf("sha256=%s,size=%d,mtime=%d",
		hex.EncodeToString(h.Sum(nil))[:16], fi.Size(), fi.ModTime().UnixNano()), nil
}
