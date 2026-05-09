// Package orchestrator runs pipelines declared via the sparkwing SDK.
// The local backend dispatches jobs as goroutines in the current
// process and persists run state to SQLite at ~/.sparkwing/.
package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
)

// Paths resolves on-disk locations under the sparkwing home root.
type Paths struct {
	Root string
}

// DefaultPaths returns paths rooted at ~/.sparkwing, honoring
// SPARKWING_HOME when set.
func DefaultPaths() (Paths, error) {
	if root := os.Getenv("SPARKWING_HOME"); root != "" {
		return PathsAt(root), nil
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
