package githooks

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

type GateState string

const (
	GateUndeclared GateState = "undeclared"

	GateArmed GateState = "armed"

	GateShadowed GateState = "shadowed"

	GateUninstalled GateState = "uninstalled"

	GateBorrowed GateState = "borrowed"
)

type RepoGates struct {
	Repo string `json:"repo"`

	HooksDir string `json:"hooks_dir,omitempty"`

	ActiveDir string `json:"active_dir,omitempty"`

	Scope string `json:"scope,omitempty"`

	Declared []string `json:"declared,omitempty"`

	Installed []string `json:"installed,omitempty"`

	Firing []string `json:"firing,omitempty"`

	Shadowed []string `json:"shadowed,omitempty"`

	Borrowed []string `json:"borrowed,omitempty"`

	Missing []string `json:"missing,omitempty"`

	State GateState `json:"state"`
}

func (r RepoGates) NotFiring() []string {
	if len(r.Shadowed) == 0 {
		return r.Missing
	}
	return sortedCopy(append(append([]string(nil), r.Shadowed...), r.Missing...))
}

var BlockingHooks = []string{"pre-commit", "pre-push"}

func (r RepoGates) Gated() bool {
	for _, name := range append(r.NotFiring(), r.Borrowed...) {
		if slices.Contains(BlockingHooks, name) {
			return false
		}
	}
	return true
}

func (r RepoGates) Summary() string {
	var parts []string
	if len(r.Borrowed) > 0 {
		parts = append(parts, fmt.Sprintf("%s fires out of %s, which is not this repo's hook directory, so nothing here declares or keeps it",
			strings.Join(r.Borrowed, ", "), r.ActiveDir))
	}
	if len(r.Shadowed) > 0 {
		parts = append(parts, fmt.Sprintf("%s installed but git reads %s (%s core.hooksPath), so nothing fires",
			strings.Join(r.Shadowed, ", "), r.ActiveDir, r.Scope))
	}
	if len(r.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("pipelines declare %s but no gate is installed", strings.Join(r.Missing, ", ")))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s: %s", r.Repo, r.State)
	}
	return r.Repo + ": " + strings.Join(parts, "; ")
}

func (r RepoGates) Remedy() string {
	if len(r.Borrowed) > 0 || (r.Scope == "local" && len(r.NotFiring()) > 0) {
		return fmt.Sprintf("git -C %s config --unset core.hooksPath, then sparkwing pipeline hooks install --repo %s", r.Repo, r.Repo)
	}
	return fmt.Sprintf("sparkwing pipeline hooks install --repo %s", r.Repo)
}

func Survey(git Git, repoRoot string, declared []string) RepoGates {
	row := RepoGates{Repo: repoRoot, Declared: sortedCopy(declared), State: GateUndeclared}
	hooksDir, err := Dir(repoRoot)
	if err != nil {
		return row
	}
	row.HooksDir = hooksDir
	row.Installed = Gates(hooksDir)

	active, scope := ActivePath(git, repoRoot)
	readDir, foreign := hooksDir, false
	if active != "" && !SameDir(active, hooksDir) {
		row.ActiveDir, row.Scope = active, scope
		readDir, foreign = active, true
	}
	row.Firing = Gates(readDir)

	if len(row.Declared) == 0 {
		return row
	}
	firing := setOf(row.Firing)
	installed := setOf(row.Installed)
	for _, n := range row.Declared {
		switch {
		case firing[n] && foreign:
			row.Borrowed = append(row.Borrowed, n)
		case firing[n]:
		case installed[n]:
			row.Shadowed = append(row.Shadowed, n)
		default:
			row.Missing = append(row.Missing, n)
		}
	}
	switch {
	case len(row.Borrowed) > 0:
		row.State = GateBorrowed
	case len(row.Shadowed) == 0 && len(row.Missing) == 0:
		row.State = GateArmed
	case len(row.Missing) == 0:
		row.State = GateShadowed
	default:
		row.State = GateUninstalled
	}
	return row
}

func SurveyFleet(git Git, repoRoots []string, declared func(repoRoot string) []string) []RepoGates {
	git = cacheMachineWideConfig(git)
	rows := make([]RepoGates, 0, len(repoRoots))
	for _, root := range repoRoots {
		var names []string
		if declared != nil {
			names = declared(root)
		}
		rows = append(rows, Survey(git, root, names))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Repo < rows[j].Repo })
	return rows
}

func Ungated(rows []RepoGates) []RepoGates {
	var out []RepoGates
	for _, r := range rows {
		if !r.Gated() {
			out = append(out, r)
		}
	}
	return out
}

func cacheMachineWideConfig(git Git) Git {
	type answer struct {
		out string
		err error
	}
	cache := map[string]answer{}
	return func(dir string, args ...string) (string, error) {
		if !slices.Contains(args, "--global") {
			return git(dir, args...)
		}
		key := strings.Join(args, "\x00")
		if a, ok := cache[key]; ok {
			return a.out, a.err
		}
		out, err := git(dir, args...)
		cache[key] = answer{out, err}
		return out, err
	}
}

func setOf(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
