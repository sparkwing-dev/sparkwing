package repos

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

type Entry struct {
	Path string `yaml:"path"`
}

type Config struct {
	Repos []*Entry `yaml:"repos,omitempty"`

	FallbackPaths []string `yaml:"fallback_paths,omitempty"`
}

func DefaultPath() (string, error) {
	if v := os.Getenv("SPARKWING_REPOS"); v != "" {
		return v, nil
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "sparkwing", "repos.yaml"), nil
	}
	if paths.UnderTest() {
		return filepath.Join(paths.TestSandbox(), "config", "sparkwing", "repos.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve repos.yaml path: %w", err)
	}
	return filepath.Join(home, ".config", "sparkwing", "repos.yaml"), nil
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

func Save(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := fssecure.EnsureDir(dir); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	buf, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal repos: %w", err)
	}
	f, err := os.CreateTemp(dir, ".repos-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
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

	cfgPath, err := DefaultPath()
	if err != nil {
		return err
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		return err
	}
	for _, e := range cfg.Repos {
		if pathsEqual(e.Path, abs) {
			return nil
		}
	}
	cfg.Repos = append(cfg.Repos, &Entry{Path: abs})
	return Save(cfgPath, cfg)
}

func Add(absPath string) error {
	abs, err := filepath.Abs(absPath)
	if err != nil {
		return fmt.Errorf("absolute %s: %w", absPath, err)
	}
	if _, err := repoKind(abs); err != nil {
		return err
	}
	cfgPath, err := DefaultPath()
	if err != nil {
		return err
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		return err
	}
	for _, e := range cfg.Repos {
		if pathsEqual(e.Path, abs) {
			return nil
		}
	}
	cfg.Repos = append(cfg.Repos, &Entry{Path: abs})
	return Save(cfgPath, cfg)
}

func Remove(match string) (int, error) {
	cfgPath, err := DefaultPath()
	if err != nil {
		return 0, err
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		return 0, err
	}
	matchAbs, _ := filepath.Abs(match)
	keep := cfg.Repos[:0]
	removed := 0
	for _, e := range cfg.Repos {
		if pathsEqual(e.Path, matchAbs) || filepath.Base(e.Path) == match {
			removed++
			continue
		}
		keep = append(keep, e)
	}
	if removed == 0 {
		return 0, nil
	}
	cfg.Repos = keep
	return removed, Save(cfgPath, cfg)
}

func Prune() ([]string, error) {
	cfgPath, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		return nil, err
	}
	var dropped []string
	keep := cfg.Repos[:0]
	for _, e := range cfg.Repos {
		if !hasSparkwingDir(e.Path) {
			dropped = append(dropped, e.Path)
			continue
		}
		keep = append(keep, e)
	}
	if len(dropped) == 0 {
		return nil, nil
	}
	cfg.Repos = keep
	return dropped, Save(cfgPath, cfg)
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
	roots := symlinkForms(os.TempDir())
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
