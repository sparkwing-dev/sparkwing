package sparkwing

import (
	"fmt"
	"os"
	"path/filepath"
)

// Path joins parts onto WorkDir() and returns the absolute path.
// The first absolute part wins -- subsequent absolute parts are
// joined onto it via filepath.Join, matching path/filepath semantics.
//
//	cfg := sparkwing.Path("backend", "config.yaml")
//	// "/repo/root/backend/config.yaml"
//
// Relative parts resolve against the process working directory whether or
// not it sits inside a Sparkwing project.
func Path(parts ...string) string {
	if len(parts) == 0 {
		return WorkDir()
	}
	if filepath.IsAbs(parts[0]) {
		return filepath.Join(parts...)
	}
	return filepath.Join(append([]string{WorkDir()}, parts...)...)
}

// ReadFile reads the named file. Relative paths resolve against
// WorkDir() (the pipeline root), matching Bash / Exec semantics.
// Absolute paths are used as-is.
//
//	data, err := sparkwing.ReadFile("backend/config.yaml")
//
// Wraps os.ReadFile.
func ReadFile(path string) ([]byte, error) {
	return os.ReadFile(resolvePath(path))
}

// WriteFile writes data to the named file with perm 0o644 (creating
// or truncating). Relative paths resolve against WorkDir(); absolute
// paths are used as-is.
//
//	err := sparkwing.WriteFile("dist/version.txt", []byte("v1.2.3"))
//
// Use os.WriteFile directly for
// non-default permissions.
func WriteFile(path string, data []byte) error {
	return os.WriteFile(resolvePath(path), data, 0o644)
}

// Glob expands a shell-style glob pattern. Relative patterns resolve
// against WorkDir(); absolute patterns are used as-is. Returns
// absolute paths so callers can hand them to other tools without
// re-joining.
//
//	manifests, err := sparkwing.Glob("k8s/*.yaml")
//
// Uses filepath.Glob, so `**` is
// NOT supported -- only `*`, `?`, and character classes. For
// repo-aware matching with `**`, use inputs.Files / inputs.RepoFiles
// in a MemoInput recipe instead.
func Glob(pattern string) ([]string, error) {
	matches, err := filepath.Glob(resolvePath(pattern))
	if err != nil {
		return nil, fmt.Errorf("sparkwing.Glob(%q): %w", pattern, err)
	}
	return matches, nil
}

func resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(WorkDir(), p)
}
