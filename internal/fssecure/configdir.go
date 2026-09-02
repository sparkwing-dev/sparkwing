package fssecure

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ConfigDirIn reports the sparkwing user configuration directory under an
// explicit home directory, ignoring XDG_CONFIG_HOME.
func ConfigDirIn(home string) string {
	return filepath.Join(home, ".config", "sparkwing")
}

// ConfigDir reports the sparkwing user configuration directory:
// $XDG_CONFIG_HOME/sparkwing when that variable is set, else
// ~/.config/sparkwing. Every writer of a sparkwing config file resolves its
// path through this helper so the directory has one owner and one mode.
func ConfigDir() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "sparkwing"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve sparkwing config directory: %w", err)
	}
	return ConfigDirIn(home), nil
}

// ConfigFile reports the path of name inside [ConfigDir].
func ConfigFile(name string) (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

var foreignConfigDirWarned sync.Map

// EnsureConfigDir prepares a directory that holds sparkwing config files.
// Inside [ConfigDir] it behaves like [EnsureDir]. An operator who points
// SPARKWING_PROFILES or SPARKWING_REPOS somewhere else owns that directory's
// mode: a missing one is still created private, but an existing group- or
// other-reachable one keeps the mode it has and the divergence is reported on
// stderr once per path.
func EnsureConfigDir(path string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode().Perm()&^DirMode != 0 && !UnderConfigDir(path) {
		if _, warned := foreignConfigDirWarned.LoadOrStore(path, true); !warned {
			fmt.Fprintf(os.Stderr, "sparkwing: %s sits outside the sparkwing config directory; leaving its permissions alone\n", path)
		}
		return nil
	}
	return EnsureDir(path)
}

// UnderConfigDir reports whether path is [ConfigDir] or sits inside it.
func UnderConfigDir(path string) bool {
	root, err := ConfigDir()
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
