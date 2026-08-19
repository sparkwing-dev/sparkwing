// Package paths resolves on-disk locations under the sparkwing home
// root. Standalone from internal/orchestrator so thin binaries
// (sparkwing-logs, sparkwing-web, sparkwing-controller, sparkwing-cache)
// can obtain the file layout without dragging in the dispatch engine.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Paths resolves on-disk locations under the sparkwing home root.
type Paths struct {
	Root string
}

// UnderTest reports whether the running binary is a Go test binary.
//
// It reads os.Args[0] instead of calling testing.Testing() because this
// package is linked into every sparkwing binary and into the SDK users
// embed, and importing testing there would pull the flag, regexp and
// pprof trees in with it. `go test` always names the binary it builds
// <pkg>.test, so the suffix is a reliable signal.
func UnderTest() bool {
	base := filepath.Base(os.Args[0])
	return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe")
}

// TestSandbox is the throwaway home a test binary is given in place of
// the user's real one. It is keyed by pid so packages that `go test`
// runs in parallel cannot collide, and it lives under os.TempDir() so
// the operating system reclaims it.
func TestSandbox() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("sparkwing-test-home-%d", os.Getpid()))
}

// DefaultPaths returns paths rooted at ~/.sparkwing, honoring
// SPARKWING_HOME when set.
//
// A test binary that set neither gets a disposable sandbox rather than
// the developer's real home. Forgetting to set SPARKWING_HOME is a
// mistake every new fixture can make once, and the cost of making it is
// a suite that writes to the machine it is validating. Redirecting rather than
// erroring keeps a read returning the empty config a fresh laptop
// legitimately has, so no test has to care.
func DefaultPaths() (Paths, error) {
	if root := os.Getenv("SPARKWING_HOME"); root != "" {
		return PathsAt(root), nil
	}
	if UnderTest() {
		return PathsAt(filepath.Join(TestSandbox(), ".sparkwing")), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return PathsAt(filepath.Join(home, ".sparkwing")), nil
}

// PathsAt roots the file layout at a specific directory.
func PathsAt(root string) Paths { return Paths{Root: root} }

// StateDB is the path to the SQLite database that holds run state.
func (p Paths) StateDB() string { return filepath.Join(p.Root, "state.db") }

// BoxSlotDir is the directory that holds the host-local concurrency
// semaphore's lock files. See internal/boxslot.
func (p Paths) BoxSlotDir() string { return filepath.Join(p.Root, "box-slots") }

// VersionStampDir holds one small file per installed sparkwing binary,
// recording the version that install last ran as. The dispatcher
// compares the stamp against the running binary to detect an upgrade
// and surface a one-time pointer at the changelog.
//
// The stamps are keyed by a digest of each binary's resolved path
// (installsite.PathKey) rather than shared, because a machine with two
// installs -- a `go install` build in GOBIN beside a source install in
// ~/.local/bin -- resolves a different one from an interactive shell
// than from a launchd job, and a shared record would be rewritten by
// each in turn, reading as upgrades and downgrades that never happened.
// The flat `last-version` file this directory supersedes is no longer
// read or written.
func (p Paths) VersionStampDir() string { return filepath.Join(p.Root, "last-version.d") }

// VersionStampFile is the version stamp for one install, keyed by its
// path digest.
func (p Paths) VersionStampFile(key string) string {
	return filepath.Join(p.VersionStampDir(), key)
}

// RunsDir is the parent directory for per-run artifacts.
func (p Paths) RunsDir() string { return filepath.Join(p.Root, "runs") }

// RunDir returns the directory for a specific run's artifacts.
func (p Paths) RunDir(runID string) string {
	return filepath.Join(p.RunsDir(), runID)
}

// NodeLog returns the path to a node's log file. Node ids may contain
// path separators; sanitizeNodeFile keeps the result portable across
// NTFS and POSIX.
func (p Paths) NodeLog(runID, nodeID string) string {
	return filepath.Join(p.RunDir(runID), sanitizeNodeFile(nodeID)+".log")
}

// EnvelopeLog returns the path to the run-level envelope event log
// (run_start, run_plan, run_finish, plan_warn, etc). This is the
// canonical persisted source for `sparkwing runs logs --follow`'s
// merged event stream. Per-node body output keeps living
// at NodeLog(); envelope events live at EnvelopeLog() so the reader
// can interleave them by timestamp without scanning every node file
// for needles. Filename starts with `_` to keep it sorted ahead of
// any node id and visually distinct in `ls`.
func (p Paths) EnvelopeLog(runID string) string {
	return filepath.Join(p.RunDir(runID), "_envelope.ndjson")
}

// Union of POSIX (/) and NTFS (\:*?"<>|) reserved filename chars.
var reservedNodeFileChars = []string{"/", `\`, ":", "*", "?", `"`, "<", ">", "|"}

func sanitizeNodeFile(nodeID string) string {
	for _, c := range reservedNodeFileChars {
		nodeID = strings.ReplaceAll(nodeID, c, "__")
	}
	return nodeID
}

// EnsureRunDir creates the on-disk layout for a run. Idempotent.
func (p Paths) EnsureRunDir(runID string) error {
	return os.MkdirAll(p.RunDir(runID), 0o755)
}

// EnsureRoot creates the sparkwing home directory if absent.
func (p Paths) EnsureRoot() error {
	return os.MkdirAll(p.Root, 0o755)
}
