// Local-mode cross-repo path resolution: maps "owner/repo" slugs to
// the operator's sibling checkouts so the local trigger consumer can
// compile + exec the right .sparkwing/.
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LocalRepoDir resolves an "owner/name" repo slug to a local checkout.
// Tries SPARKWING_REPO_<UPPER_SNAKE_NAME> first, then $HOME/code/<name>.
// Errors when neither resolves to a directory containing .git -- fail-
// fast at claim time instead of a silent compile-loop timeout later.
func LocalRepoDir(slug string) (string, error) {
	owner, name, ok := splitRepoSlug(slug)
	if !ok {
		return "", fmt.Errorf("local repo: malformed slug %q (want owner/name)", slug)
	}
	_ = owner

	if env := os.Getenv("SPARKWING_REPO_" + envKeyForName(name)); env != "" {
		if err := assertGitDir(env); err != nil {
			return "", fmt.Errorf("local repo: SPARKWING_REPO_%s=%s: %w",
				envKeyForName(name), env, err)
		}
		return env, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("local repo: resolve home: %w", err)
	}
	dir := filepath.Join(home, "code", name)
	if err := assertGitDir(dir); err != nil {
		return "", fmt.Errorf("local repo: %s not found at %s (clone it or set SPARKWING_REPO_%s): %w",
			slug, dir, envKeyForName(name), err)
	}
	return dir, nil
}

// splitRepoSlug parses "owner/name"; ok=false on empty halves.
func splitRepoSlug(slug string) (owner, name string, ok bool) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "", "", false
	}
	i := strings.IndexByte(slug, '/')
	if i <= 0 || i == len(slug)-1 {
		return "", "", false
	}
	return slug[:i], slug[i+1:], true
}

// envKeyForName converts "sparks-core" to "SPARKS_CORE": hyphens to
// underscores, dots dropped, rest upper-cased.
func envKeyForName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteByte('_')
		}
	}
	return b.String()
}

// assertGitDir errors when path lacks a .git entry.
func assertGitDir(path string) error {
	if path == "" {
		return errors.New("empty path")
	}
	fi, err := os.Stat(filepath.Join(path, ".git"))
	if err != nil {
		return err
	}
	// .git is a directory for normal clones, regular file for
	// worktrees/submodules.
	if fi.IsDir() || fi.Mode().IsRegular() {
		return nil
	}
	return fmt.Errorf("%s/.git is not a directory or file", path)
}
