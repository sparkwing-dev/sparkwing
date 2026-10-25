package repos

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

type entry struct {
	Path string `yaml:"path"`
}

type config struct {
	Repos []*entry `yaml:"repos,omitempty"`

	FallbackPaths []string `yaml:"fallback_paths,omitempty"`
}

// defaultPath reports the config.yaml the repo registry is read from and
// written to; see [userconfig.Path].
func defaultPath() (string, error) {
	return userconfig.Path()
}

// load reads the repos section of the config.yaml at path. An absent file or
// section is an empty registry.
func load(path string) (*config, error) {
	var cfg config
	if _, err := userconfig.Read(path, userconfig.Repos, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// safety: the registry changes under the settings file's lock, so two
// registrations racing each keep their entry.
func update(change func(cfg *config) (bool, error)) error {
	path, err := defaultPath()
	if err != nil {
		return err
	}
	var cfg config
	errUnchanged := errors.New("unchanged")
	err = userconfig.Update(path, userconfig.Repos, "the repo registry", &cfg, func(bool) error {
		changed, err := change(&cfg)
		if err == nil && !changed {
			return errUnchanged
		}
		return err
	})
	if errors.Is(err, errUnchanged) {
		return nil
	}
	return err
}

func Add(absPath string) error {
	abs, err := filepath.Abs(absPath)
	if err != nil {
		return fmt.Errorf("absolute %s: %w", absPath, err)
	}
	if _, err := repoKind(abs); err != nil {
		return err
	}
	return update(func(cfg *config) (bool, error) {
		for _, e := range cfg.Repos {
			if pathsEqual(e.Path, abs) {
				return false, nil
			}
		}
		cfg.Repos = append(cfg.Repos, &entry{Path: abs})
		return true, nil
	})
}

func Remove(match string) (int, error) {
	matchAbs, _ := filepath.Abs(match)
	removed := 0
	err := update(func(cfg *config) (bool, error) {
		keep := cfg.Repos[:0]
		for _, e := range cfg.Repos {
			if pathsEqual(e.Path, matchAbs) || filepath.Base(e.Path) == match {
				removed++
				continue
			}
			keep = append(keep, e)
		}
		cfg.Repos = keep
		return removed > 0, nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

func Prune() ([]string, error) {
	var dropped []string
	err := update(func(cfg *config) (bool, error) {
		keep := cfg.Repos[:0]
		for _, e := range cfg.Repos {
			if !hasSparkwingDir(e.Path) {
				dropped = append(dropped, e.Path)
				continue
			}
			keep = append(keep, e)
		}
		cfg.Repos = keep
		return len(dropped) > 0, nil
	})
	if err != nil {
		return nil, err
	}
	return dropped, nil
}

type ListEntry struct {
	Path     string
	Status   string
	Worktree bool
}

func List() ([]ListEntry, error) {
	cfgPath, err := defaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := load(cfgPath)
	if err != nil {
		return nil, err
	}
	out := make([]ListEntry, 0, len(cfg.Repos))
	for _, e := range cfg.Repos {
		le := ListEntry{Path: e.Path, Status: "ok"}
		kind, kerr := repoKind(e.Path)
		switch {
		case kerr != nil:
			le.Status = "stale"
		case !hasSparkwingDir(e.Path):
			le.Status = "stale"
		case kind == repoKindWorktree:
			le.Worktree = true
		}
		out = append(out, le)
	}
	return out, nil
}

type Candidate struct {
	Path     string
	Worktree bool
}

func CandidatePaths() ([]Candidate, error) {
	cfgPath, err := defaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := load(cfgPath)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Candidate
	add := func(p string) {
		abs, err := filepath.Abs(p)
		if err != nil || seen[abs] {
			return
		}
		if !hasSparkwingDir(abs) {
			return
		}
		kind, _ := repoKind(abs)
		seen[abs] = true
		out = append(out, Candidate{Path: abs, Worktree: kind == repoKindWorktree})
	}
	for _, e := range cfg.Repos {
		add(expandHome(e.Path))
	}
	for _, fp := range cfg.FallbackPaths {
		fp = expandHome(fp)
		entries, err := os.ReadDir(fp)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			add(filepath.Join(fp, n))
		}
	}
	return out, nil
}

type repoKindEnum int

const (
	repoKindMissing repoKindEnum = iota
	repoKindRegular
	repoKindWorktree
)

func repoKind(absPath string) (repoKindEnum, error) {
	if absPath == "" {
		return repoKindMissing, errors.New("empty path")
	}
	gitPath := filepath.Join(absPath, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return repoKindMissing, fmt.Errorf("%s: %w", absPath, err)
	}
	if fi.IsDir() {
		return repoKindRegular, nil
	}
	if fi.Mode().IsRegular() {
		return repoKindWorktree, nil
	}
	return repoKindMissing, fmt.Errorf("%s/.git: unexpected mode %v", absPath, fi.Mode())
}

func hasSparkwingDir(absPath string) bool {
	fi, err := os.Stat(filepath.Join(absPath, ".sparkwing"))
	if err != nil {
		return false
	}
	return fi.IsDir()
}

func pathsEqual(a, b string) bool {
	ca := filepath.Clean(a)
	cb := filepath.Clean(b)
	if ca == cb {
		return true
	}
	if ra, err := filepath.EvalSymlinks(ca); err == nil {
		ca = ra
	}
	if rb, err := filepath.EvalSymlinks(cb); err == nil {
		cb = rb
	}
	return ca == cb
}

func expandHome(p string) string {
	if p == "" {
		return p
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~/"))
	}
	return p
}
