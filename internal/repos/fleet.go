package repos

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

// sdkModulePath is the SDK module every sparkwing pipeline pins.
const sdkModulePath = "github.com/sparkwing-dev/sparkwing"

// Git runs a git subcommand inside dir and returns stdout. The fleet
// derivation takes it as a dependency so tests can canonicalize
// worktrees against a fake instead of a real checkout.
type Git func(dir string, args ...string) (string, error)

// RunObservation is the repo identity carried on an observed run,
// projected out of the runs store. It supplies "last run" enrichment
// and surfaces repos that have executed pipelines but aren't in
// repos.yaml.
type RunObservation struct {
	Repo     string
	RepoURL  string
	Pipeline string
	At       time.Time
}

// WorktreeRef is one linked worktree of a primary repo, kept only to
// report a pin that diverges from the primary's -- a signal the
// operator bumped a worktree in isolation.
type WorktreeRef struct {
	Path string
	Pin  string
}

// Repo is one row of the fleet: a primary checkout (or a runs-only
// observation) with its SDK pin, last run, and any divergent
// worktrees. Update operates on Primary only.
type Repo struct {
	// Primary is the canonical primary checkout path. Empty for a
	// runs-only row (observed in the store, no local checkout).
	Primary string
	Name    string
	// Pin is the SDK version in Primary/.sparkwing/go.mod, or "" when
	// unresolved (missing module, replace directive, runs-only).
	Pin string
	// Replace is the SDK replace target when the project replaces the
	// SDK with a local path; such repos are not bumpable.
	Replace      string
	LastRun      time.Time
	LastPipeline string
	Worktrees    []WorktreeRef
	Status       string // ok | worktree-only | runs-only
	GuidesBehind int
	Latest       string
}

// DivergentWorktrees returns the worktrees whose pin differs from the
// primary's, i.e. the ones worth a detail line in the report.
func (r Repo) DivergentWorktrees() []WorktreeRef {
	var out []WorktreeRef
	for _, w := range r.Worktrees {
		if w.Pin != "" && w.Pin != r.Pin {
			out = append(out, w)
		}
	}
	return out
}

// PrimaryRoot canonicalizes any checkout path to its primary repo
// root by resolving git's common dir: a linked worktree's common dir
// points at the primary's .git, so the primary root is that dir's
// parent. A regular checkout resolves to itself. The result is
// symlink-resolved so two spellings of the same repo dedupe.
func PrimaryRoot(git Git, path string) (string, error) {
	out, err := git(path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		out, err = git(path, "rev-parse", "--git-common-dir")
		if err != nil {
			return "", err
		}
	}
	common := strings.TrimSpace(out)
	if common == "" {
		return canonPath(path), nil
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(path, common)
	}
	common = filepath.Clean(common)
	root := filepath.Dir(common)
	return canonPath(root), nil
}

// canonPath cleans and symlink-resolves a path for stable dedup,
// falling back to the cleaned path when the target can't be resolved.
func canonPath(p string) string {
	c := filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(c); err == nil {
		return r
	}
	return c
}

// SDKPin reads the SDK version pinned in sparkwingDir/go.mod. It
// returns the require version, the replace target (when the SDK is
// replaced with a local module), or empty strings when neither is
// present or the file can't be parsed.
func SDKPin(sparkwingDir string) (pin, replace string) {
	body, err := os.ReadFile(filepath.Join(sparkwingDir, "go.mod"))
	if err != nil {
		return "", ""
	}
	mf, err := modfile.Parse("go.mod", body, nil)
	if err != nil {
		return "", ""
	}
	for _, req := range mf.Require {
		if req.Mod.Path == sdkModulePath {
			pin = req.Mod.Version
		}
	}
	for _, rep := range mf.Replace {
		if rep.Old.Path == sdkModulePath {
			replace = rep.New.Path
			if rep.New.Version != "" {
				replace += "@" + rep.New.Version
			}
		}
	}
	return pin, replace
}

// DeriveFleet builds the deduped fleet from registered candidates and
// observed runs. Candidates are canonicalized to their primary repo
// via git; a primary is tracked once, with any additional worktrees
// recorded for divergent-pin reporting. Runs that match no candidate
// become runs-only rows. latest, when a valid semver, drives the
// "guides behind" count via guidesBehind.
func DeriveFleet(cands []Candidate, runs []RunObservation, git Git, latest string, guidesBehind func(pin, latest string) int) []Repo {
	byPrimary := map[string]*Repo{}
	order := []string{}

	for _, c := range cands {
		primary, err := PrimaryRoot(git, c.Path)
		if err != nil || primary == "" {
			primary = canonPath(c.Path)
		}
		r, ok := byPrimary[primary]
		if !ok {
			pin, replace := SDKPin(filepath.Join(primary, ".sparkwing"))
			r = &Repo{
				Primary: primary,
				Name:    filepath.Base(primary),
				Pin:     pin,
				Replace: replace,
				Status:  "ok",
				Latest:  latest,
			}
			if pin != "" && guidesBehind != nil {
				r.GuidesBehind = guidesBehind(pin, latest)
			}
			byPrimary[primary] = r
			order = append(order, primary)
		}
		if canonPath(c.Path) != primary {
			pin, _ := SDKPin(filepath.Join(canonPath(c.Path), ".sparkwing"))
			r.Worktrees = append(r.Worktrees, WorktreeRef{Path: canonPath(c.Path), Pin: pin})
		}
	}

	attachRuns(byPrimary, order, runs)
	out := runsOnly(byPrimary, runs, latest)

	for _, p := range order {
		out = append(out, *byPrimary[p])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return fleetStatusRank(out[i].Status) < fleetStatusRank(out[j].Status)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func fleetStatusRank(s string) int {
	switch s {
	case "runs-only":
		return 1
	default:
		return 0
	}
}

// attachRuns stamps the most recent matching run onto each primary
// repo. A run matches a repo when its short name equals the repo's
// directory name or its remote basename.
func attachRuns(byPrimary map[string]*Repo, order []string, runs []RunObservation) {
	for _, p := range order {
		r := byPrimary[p]
		for _, obs := range runs {
			if !runMatchesRepo(obs, *r) {
				continue
			}
			if obs.At.After(r.LastRun) {
				r.LastRun = obs.At
				r.LastPipeline = obs.Pipeline
			}
		}
	}
}

func runMatchesRepo(obs RunObservation, r Repo) bool {
	if obs.Repo == "" {
		return false
	}
	if strings.EqualFold(obs.Repo, r.Name) {
		return true
	}
	if obs.RepoURL != "" {
		base := strings.TrimSuffix(filepath.Base(obs.RepoURL), ".git")
		if strings.EqualFold(base, r.Name) {
			return true
		}
	}
	return false
}

// runsOnly returns rows for repos observed in the store but not backed
// by any registered checkout, so the fleet still names them (they're
// skipped by update since there's nothing to bump).
func runsOnly(byPrimary map[string]*Repo, runs []RunObservation, latest string) []Repo {
	haveName := map[string]bool{}
	for _, r := range byPrimary {
		haveName[strings.ToLower(r.Name)] = true
	}
	agg := map[string]*Repo{}
	var names []string
	for _, obs := range runs {
		if obs.Repo == "" || haveName[strings.ToLower(obs.Repo)] {
			continue
		}
		key := strings.ToLower(obs.Repo)
		r, ok := agg[key]
		if !ok {
			r = &Repo{Name: obs.Repo, Status: "runs-only", Latest: latest}
			agg[key] = r
			names = append(names, key)
		}
		if obs.At.After(r.LastRun) {
			r.LastRun = obs.At
			r.LastPipeline = obs.Pipeline
		}
	}
	var out []Repo
	for _, k := range names {
		out = append(out, *agg[k])
	}
	return out
}

// GuidesBehind counts the embedded migration guides strictly newer
// than pin and no newer than latest -- the "versions behind" measure
// surfaced on the fleet list. Returns 0 when either bound isn't a
// valid semver.
func GuidesBehind(guideVersions []string, pin, latest string) int {
	if !semver.IsValid(pin) || !semver.IsValid(latest) {
		return 0
	}
	n := 0
	for _, v := range guideVersions {
		if !semver.IsValid(v) {
			continue
		}
		if semver.Compare(v, pin) > 0 && semver.Compare(v, latest) <= 0 {
			n++
		}
	}
	return n
}
