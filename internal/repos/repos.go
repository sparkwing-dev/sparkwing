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

type Entry struct {
	Path string `yaml:"path"`
}

type Config struct {
	Repos []*Entry `yaml:"repos,omitempty"`

	FallbackPaths []string `yaml:"fallback_paths,omitempty"`
}

// DefaultPath reports the config.yaml the repo registry is read from and
// written to; see [userconfig.Path].
func DefaultPath() (string, error) {
	return userconfig.Path()
}

// Load reads the repos section of the config.yaml at path. An absent file or
// section is an empty registry.
func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := userconfig.Read(path, userconfig.Repos, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save replaces the repos section of the config.yaml at path with cfg,
// keeping every other section; see [userconfig.Write].
func Save(path string, cfg *Config) error {
	return userconfig.Write(path, userconfig.Repos, "the repo registry", cfg)
}

// safety: the registry changes under the settings file's lock, so two
// registrations racing each keep their entry.
func update(change func(cfg *Config) (bool, error)) error {
	path, err := DefaultPath()
	if err != nil {
		return err
	}
	var cfg Config
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

func AutoRegister(absPath string) error {
	if os.Getenv("SPARKWING_NO_AUTO_REGISTER") == "1" {
		return nil
	}
	if absPath == "" {
		return errors.New("AutoRegister: empty path")
	}
	abs, err := filepath.Abs(absPath)
	if err != nil {
		return fmt.Errorf("absolute %s: %w", absPath, err)
	}
	if underTempDir(abs) {
		return nil
	}
	kind, err := repoKind(abs)
	if err != nil {
		return err
	}
	if kind == repoKindWorktree && os.Getenv("SPARKWING_AUTO_REGISTER_WORKTREES") != "1" {
		return nil
	}

	return update(func(cfg *Config) (bool, error) {
		for _, e := range cfg.Repos {
			if pathsEqual(e.Path, abs) {
				return false, nil
			}
		}
		cfg.Repos = append(cfg.Repos, &Entry{Path: abs})
		return true, nil
	})
}

func Add(absPath string) error {
	abs, err := filepath.Abs(absPath)
	if err != nil {
		return fmt.Errorf("absolute %s: %w", absPath, err)
	}
	if _, err := repoKind(abs); err != nil {
		return err
	}
	return update(func(cfg *Config) (bool, error) {
		for _, e := range cfg.Repos {
			if pathsEqual(e.Path, abs) {
				return false, nil
			}
		}
		cfg.Repos = append(cfg.Repos, &Entry{Path: abs})
		return true, nil
	})
}

func Remove(match string) (int, error) {
	matchAbs, _ := filepath.Abs(match)
	removed := 0
	err := update(func(cfg *Config) (bool, error) {
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
	err := update(func(cfg *Config) (bool, error) {
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
	cfgPath, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := Load(cfgPath)
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

func FallbackDirs() ([]string, error) {
	cfgPath, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cfg.FallbackPaths))
	for _, p := range cfg.FallbackPaths {
		exp := expandHome(p)
		out = append(out, exp)
	}
	return out, nil
}

type Candidate struct {
	Path     string
	Worktree bool
}

func CandidatePaths() ([]Candidate, error) {
	cfgPath, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := Load(cfgPath)
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

func underTempDir(abs string) bool {
	// macOS hands out a per-user TMPDIR, so scratch trees under the shared
	// /tmp (really /private/tmp) slipped past a TMPDIR-only check. The
	// resolved form is named too, so the rule reads the same on a host
	// where /tmp is a plain directory and /private/tmp does not exist.
	roots := append(symlinkForms(os.TempDir()), symlinkForms("/tmp")...)
	roots = append(roots, symlinkForms("/private/tmp")...)
	targets := symlinkForms(abs)
	for _, root := range roots {
		for _, target := range targets {
			if withinDir(root, target) {
				return true
			}
		}
	}
	return false
}

func symlinkForms(p string) []string {
	out := []string{filepath.Clean(p)}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		if c := filepath.Clean(r); c != out[0] {
			out = append(out, c)
		}
	}
	return out
}

func withinDir(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
