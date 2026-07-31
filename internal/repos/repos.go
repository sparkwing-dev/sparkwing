// Package repos manages the laptop's registry of sparkwing-bearing
// checkouts. The cluster's controller knows which workers are wired
// to which repos via the agent token; locally, there's no such
// authority, so we keep an explicit list at
// ~/.config/sparkwing/repos.yaml plus optional fallback search paths.
package repos

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// Entry is one registered repo. Path is the only required field;
// future fields (e.g. labels, default-branch override) land here
// without breaking the YAML shape.
type Entry struct {
	Path string `yaml:"path"`
}

// Config is the on-disk repos.yaml shape.
type Config struct {
	// Repos are the explicitly registered checkouts. Order is
	// preserved -- first match wins in resolution, so users can
	// promote a primary checkout above feature worktrees they
	// also registered explicitly.
	Repos []*Entry `yaml:"repos,omitempty"`

	// FallbackPaths are directories scanned for `*/.sparkwing/`
	// subdirectories when nothing in Repos matches a pipeline
	// lookup. Empty by default; a power user adding `~/code` here
	// gets the legacy "sibling checkout convention" back as an
	// opt-in.
	FallbackPaths []string `yaml:"fallback_paths,omitempty"`
}

// DefaultPath returns the resolved repos.yaml path. Honors
// SPARKWING_REPOS > XDG_CONFIG_HOME > $HOME, mirroring profile.go.
//
// A test binary that set neither override gets a disposable sandbox
// instead of the developer's registry. This file is the laptop's fleet
// registry, so a fixture that writes it does not just dirty a scratch
// file: it decides what `hooks survey` and every --fleet sweep can see.
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

// Load reads repos.yaml at path. Missing file returns an empty
// Config without error -- a fresh laptop hasn't registered anything
// yet, and that's a normal state, not an error.
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

// Save writes cfg to path atomically: marshal, write a private temp
// file, fsync it, rename over the target. Creates parent dirs as needed.
// 0644 because repos.yaml is just pointers to checkouts -- no secrets,
// fine for casual sharing.
//
// The temp file gets a unique name from os.CreateTemp. It used to be the
// fixed path+".tmp", which made the rename atomic against a crash but
// not against a second writer. Concurrent registration is normal here,
// because every `sparkwing run` auto-registers on startup and the
// orchestrator runs pipeline jobs in parallel, and with one shared
// staging name those writers collide: each opens it with O_TRUNC, and
// the first rename moves it out from under everyone else, so the losers
// fail with ENOENT. main.go discards AutoRegister's error, so that loss
// was silent. The regression test beside this file reproduces it against
// the old code on every run.
//
// Whether the same collision is what corrupted the registry BW-1457 was
// opened over is not settled. Two writers sharing a descriptor target
// can strand the tail of a longer write past the end of a shorter one,
// which is the shape of the damage found (a stray "e" after the
// fallback_paths key, where the file's last entry is "- ~/code"), but a
// direct reproduction of the byte interleaving did not fire on APFS,
// where each write lands in one syscall. The unique name removes the
// whole class either way.
//
// The fsync is what makes the rename mean anything after a power loss:
// without it the directory entry can land while the contents are still
// in the page cache.
func Save(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
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

// AutoRegister records absPath in repos.yaml if it isn't already
// there. Idempotent. Skips worktree checkouts (.git is a regular
// file, not a directory) unless SPARKWING_AUTO_REGISTER_WORKTREES=1
// is set -- a feature-branch worktree shouldn't silently shadow
// main's pipelines for cross-repo lookups, but power users who
// orchestrate from worktrees can opt in.
//
// It also skips checkouts under the system temp directory, because a
// scaffolded repo there is deleted minutes later and the entry it leaves
// can never resolve again. 307 of the 457 entries in the registry
// BW-1457 was opened over were exactly that: scratch repos the
// template-verify pipeline job creates with os.MkdirTemp, runs
// `sparkwing run` inside, and removes.
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

// Add registers absPath explicitly. Unlike AutoRegister, this
// surfaces errors directly and does NOT skip worktrees -- the user
// asked for it. Idempotent.
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

// Remove drops every entry whose path equals (or whose basename
// equals) match. Returns the number of removed entries; zero is
// not an error. Lets the operator say `sparkwing pipeline remove
// sparkwing` without typing the full path.
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

// Prune drops every entry whose path no longer points at a
// .sparkwing/-bearing checkout (deleted, renamed, .sparkwing/ dir
// removed). Returns the dropped paths. Useful as a one-shot tidy
// when the laptop's checkout layout has shifted.
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

// List returns the current registry, with each entry's status
// classified for display: "ok", "stale" (path missing or has no
// .sparkwing/), "worktree" (registered explicitly via Add). Used
// by the `sparkwing pipeline list` CLI.
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

// FallbackDirs returns the registered fallback search paths. Used
// by the resolver when an explicit-repos lookup misses.
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

// CandidatePaths returns every path the resolver should consider for
// a pipeline lookup: explicit repos first (in declaration order),
// then a deduped scan of fallback_paths/*/.sparkwing/. Stale paths
// (missing .sparkwing/) are filtered out. Worktrees stay in the list
// but are tagged so the resolver can deprioritize them on tie.
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

// repoKind classifies a path's git checkout flavor: regular (own
// .git/ dir), worktree (.git is a file pointer to the parent's
// .git/worktrees/<name>/), or missing.
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

// hasSparkwingDir is a cheaper-than-Stat existence check used in
// the hot path of CandidatePaths. Just checks for a directory
// entry; doesn't validate that it parses as Go.
func hasSparkwingDir(absPath string) bool {
	fi, err := os.Stat(filepath.Join(absPath, ".sparkwing"))
	if err != nil {
		return false
	}
	return fi.IsDir()
}

// underTempDir reports whether abs sits inside the system temp
// directory.
//
// Each side is compared in both its raw and its symlink-resolved form,
// because macOS hands out /var/folders/... temp paths that are really
// /private/var/folders/..., and EvalSymlinks only resolves a path that
// already exists. A checkout sparkwing is about to create resolves to
// nothing, so matching resolved-against-resolved alone would miss
// exactly the scratch directories this filter is for.
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

// symlinkForms returns the cleaned path and, when it resolves, its
// symlink-resolved twin.
func symlinkForms(p string) []string {
	out := []string{filepath.Clean(p)}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		if c := filepath.Clean(r); c != out[0] {
			out = append(out, c)
		}
	}
	return out
}

// withinDir reports whether target is dir or sits beneath it.
func withinDir(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// pathsEqual compares two filesystem paths after Clean+Abs+
// EvalSymlinks. Avoids "registered twice" because of trailing
// slashes or symlink-vs-real path differences. EvalSymlinks
// failures fall back to the cleaned path comparison so a missing
// dir doesn't error -- the caller has already validated existence
// when it matters.
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

// expandHome resolves a leading ~ or ~/ prefix to the user's home.
// Any other shell-isms (env interpolation, $VAR) are left alone --
// repos.yaml is hand-edited config, and surprising expansion would
// be worse than keeping `~` as the only sugar.
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
