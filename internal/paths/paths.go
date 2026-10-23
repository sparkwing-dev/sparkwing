package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

type Paths struct {
	Root string
}

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

// StateDirs lists every directory that may hold runtime state: the home in
// use and the default ~/.sparkwing, which keeps an earlier home's state after
// SPARKWING_HOME moves the home elsewhere.
func StateDirs() []string {
	var dirs []string
	if p, err := DefaultPaths(); err == nil {
		dirs = append(dirs, filepath.Clean(p.Root))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".sparkwing"))
	}
	return dirs
}

func isOperatorHome(root string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return filepath.Clean(root) == filepath.Join(home, ".sparkwing")
}

// SandboxHome reports the sparkwing home in use when it is not the operator's
// own ~/.sparkwing, so a caller can refuse to write outside it. A
// SPARKWING_HOME redirect answers true, because it is not the home whose state
// the operator keeps.
func SandboxHome() (string, bool) {
	p, err := DefaultPaths()
	if err != nil || p.Root == "" || isOperatorHome(p.Root) {
		return "", false
	}
	return p.Root, true
}

func PathsAt(root string) Paths { return Paths{Root: root} }

func (p Paths) StateDB() string { return filepath.Join(p.Root, "state.db") }

// SessionLedgerDir holds one record per step command still running, written
// by the node that started it, so a sweep can end sessions whose node died
// without reaping them.
func (p Paths) SessionLedgerDir() string { return filepath.Join(p.Root, "sessions") }

// refWorktreesDir holds the git worktrees `run --sw-detached --sw-ref` checks out for
// detached runs. Every one sits under this root, which is what lets a sweep
// decide ownership by containment rather than by a record it has to trust.
func (p Paths) refWorktreesDir() string { return filepath.Join(p.Root, "ref-worktrees") }

// standaloneDir holds the runs stores written by pipeline binaries that could
// not reach the admission daemon. The read verbs merge these with
// [Paths.StateDB]; [Paths.StandaloneStores] enumerates them.
func (p Paths) standaloneDir() string { return filepath.Join(p.Root, "standalone") }

// standaloneStateDB is the store standalone runs share.
func (p Paths) standaloneStateDB() string {
	return filepath.Join(p.standaloneDir(), "state.db")
}

// standaloneSchemaDir is where a build before the store baseline kept a
// standalone store the shared one refused, one directory per schema version.
// Nothing writes there now; [Paths.StandaloneStores] still lists what is left.
func (p Paths) standaloneSchemaDir(version int) string {
	return filepath.Join(p.standaloneDir(), fmt.Sprintf("schema-%d", version))
}

func standaloneSchemaOf(entry os.DirEntry) (int, bool) {
	if !entry.IsDir() {
		return 0, false
	}
	rest, found := strings.CutPrefix(entry.Name(), "schema-")
	if !found {
		return 0, false
	}
	schema, err := strconv.Atoi(rest)
	if err != nil || schema <= 0 {
		return 0, false
	}
	return schema, true
}

// safety: Lstat, because a symlink into another home's store is not this
// home's standalone run and must not be read as one.
func isRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func (p Paths) versionStampDir() string { return filepath.Join(p.Root, "last-version.d") }

func (p Paths) VersionStampFile(key string) string {
	return filepath.Join(p.versionStampDir(), key)
}

func (p Paths) RunsDir() string { return filepath.Join(p.Root, "runs") }

func (p Paths) RunDir(runID string) string {
	return filepath.Join(p.RunsDir(), runID)
}

func (p Paths) ensureRunDir(runID string) error {
	if err := p.EnsureRoot(); err != nil {
		return err
	}
	if err := fssecure.EnsureDir(p.RunsDir()); err != nil {
		return err
	}
	return fssecure.EnsureDir(p.RunDir(runID))
}

func (p Paths) EnsureRoot() error {
	return fssecure.EnsureDir(p.Root)
}
