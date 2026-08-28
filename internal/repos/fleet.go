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

const sdkModulePath = "github.com/sparkwing-dev/sparkwing"

type Git func(dir string, args ...string) (string, error)

type RunObservation struct {
	Repo     string
	RepoURL  string
	Pipeline string
	At       time.Time
}

type WorktreeRef struct {
	Path string
	Pin  string
}

type Repo struct {
	Primary string
	Name    string

	Pin string

	Replace      string
	LastRun      time.Time
	LastPipeline string
	Worktrees    []WorktreeRef
	Status       string
	GuidesBehind int
	Latest       string
}

func (r Repo) DivergentWorktrees() []WorktreeRef {
	var out []WorktreeRef
	for _, w := range r.Worktrees {
		if w.Pin != "" && w.Pin != r.Pin {
			out = append(out, w)
		}
	}
	return out
}

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

func canonPath(p string) string {
	c := filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(c); err == nil {
		return r
	}
	return c
}

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

func SDKWorkspaceOverride(sparkwingDir string) string {
	body, err := os.ReadFile(filepath.Join(sparkwingDir, "go.work"))
	if err != nil {
		return ""
	}
	wf, err := modfile.ParseWork("go.work", body, nil)
	if err != nil {
		return ""
	}
	for _, use := range wf.Use {
		dir := use.Path
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(sparkwingDir, dir)
		}
		if modulePathOf(dir) == sdkModulePath {
			return filepath.Clean(dir)
		}
	}
	return ""
}

func modulePathOf(dir string) string {
	body, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	mf, err := modfile.Parse("go.mod", body, nil)
	if err != nil || mf.Module == nil {
		return ""
	}
	return mf.Module.Mod.Path
}

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

func RunMatchesRepo(obs RunObservation, r Repo) bool {
	return runMatchesRepo(obs, r)
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
