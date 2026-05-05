package sparkwing

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNoProject is returned by SDK helpers that need a project root
// when no `.sparkwing/` was found above cwd at init. errors.Is-check
// to distinguish "we aren't in a sparkwing project" from other I/O
// errors. Path() panics with the same message.
var ErrNoProject = errors.New("sparkwing: no .sparkwing/ project found above cwd")

// Path joins parts onto WorkDir() and returns the absolute path.
// The first absolute part wins -- subsequent absolute parts are
// joined onto it via filepath.Join, matching path/filepath semantics.
//
//	cfg := sparkwing.Path("backend", "config.yaml")
//	// "/repo/root/backend/config.yaml"
//
// Panics with ErrNoProject if no `.sparkwing/` was discoverable
// above cwd at init AND the resulting path would be relative
// (i.e. the first part is not absolute). Pure-absolute joins are
// safe to call from any context.
func Path(parts ...string) string {
	if len(parts) == 0 {
		return requireWorkDir()
	}
	if filepath.IsAbs(parts[0]) {
		return filepath.Join(parts...)
	}
	return filepath.Join(append([]string{requireWorkDir()}, parts...)...)
}

// ReadFile reads the named file. Relative paths resolve against
// WorkDir() (the pipeline root), matching ShIn / ExecIn semantics.
// Absolute paths are used as-is.
//
//	data, err := sparkwing.ReadFile("backend/config.yaml")
//
// Returns ErrNoProject (wrapped) if the path is relative and no
// `.sparkwing/` was found above cwd. Wraps os.ReadFile.
func ReadFile(path string) ([]byte, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return nil, fmt.Errorf("sparkwing.ReadFile(%q): %w", path, err)
	}
	return os.ReadFile(resolved)
}

// WriteFile writes data to the named file with perm 0o644 (creating
// or truncating). Relative paths resolve against WorkDir(); absolute
// paths are used as-is.
//
//	err := sparkwing.WriteFile("dist/version.txt", []byte("v1.2.3"))
//
// Returns ErrNoProject (wrapped) if the path is relative and no
// `.sparkwing/` was found above cwd. Use os.WriteFile directly for
// non-default permissions.
func WriteFile(path string, data []byte) error {
	resolved, err := resolvePath(path)
	if err != nil {
		return fmt.Errorf("sparkwing.WriteFile(%q): %w", path, err)
	}
	return os.WriteFile(resolved, data, 0o644)
}

// Glob expands a shell-style glob pattern. Relative patterns resolve
// against WorkDir(); absolute patterns are used as-is. Returns
// absolute paths so callers can hand them to other tools without
// re-joining.
//
//	manifests, err := sparkwing.Glob("k8s/*.yaml")
//
// Returns ErrNoProject (wrapped) if the pattern is relative and no
// `.sparkwing/` was found above cwd. Uses filepath.Glob, so `**` is
// NOT supported -- only `*`, `?`, and character classes. For
// repo-aware matching with `**`, use inputs.Files / inputs.RepoFiles
// in a CacheKeyFn instead.
func Glob(pattern string) ([]string, error) {
	resolved, err := resolvePath(pattern)
	if err != nil {
		return nil, fmt.Errorf("sparkwing.Glob(%q): %w", pattern, err)
	}
	matches, err := filepath.Glob(resolved)
	if err != nil {
		return nil, fmt.Errorf("sparkwing.Glob(%q): %w", pattern, err)
	}
	return matches, nil
}

// requireWorkDir returns WorkDir() or panics with ErrNoProject.
func requireWorkDir() string {
	wd := WorkDir()
	if wd == "" {
		panic(ErrNoProject)
	}
	return wd
}

// resolvePath is the shared relative->WorkDir resolution. Absolute
// input passes through unchanged; relative input returns ErrNoProject
// when WorkDir is empty.
func resolvePath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	wd := WorkDir()
	if wd == "" {
		return "", ErrNoProject
	}
	if p == "" {
		return wd, nil
	}
	return filepath.Join(wd, p), nil
}
