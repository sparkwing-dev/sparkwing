package githooks

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// GateState is what git does with the hooks a repository's pipelines ask for.
type GateState string

const (
	// GateUndeclared means no pipeline asks for a git hook, so the
	// repository has no gate to arm and none to lose.
	GateUndeclared GateState = "undeclared"
	// GateArmed means git runs a sparkwing gate for every hook the
	// repository declares.
	GateArmed GateState = "armed"
	// GateShadowed means the gates are installed but core.hooksPath sends
	// git to a directory that does not hand off to them, so a commit or
	// push runs nothing.
	GateShadowed GateState = "shadowed"
	// GateUninstalled means the repository declares a hook no managed gate
	// was ever written for.
	GateUninstalled GateState = "uninstalled"
)

// RepoGates is one repository's row in a fleet survey: the hooks its
// pipelines ask git to run, the ones installed, the ones git actually reads,
// and the verdict that follows.
type RepoGates struct {
	// Repo is the checkout root surveyed.
	Repo string `json:"repo"`
	// HooksDir is the repository's own hook directory.
	HooksDir string `json:"hooks_dir,omitempty"`
	// ActiveDir is the directory git reads hooks from when a core.hooksPath
	// override redirects it, empty when git reads HooksDir.
	ActiveDir string `json:"active_dir,omitempty"`
	// Scope is the git config scope that set ActiveDir, "local" or "global".
	Scope string `json:"scope,omitempty"`
	// Declared are the hook names the repository's pipelines ask for.
	Declared []string `json:"declared,omitempty"`
	// Installed are the managed gates in HooksDir.
	Installed []string `json:"installed,omitempty"`
	// Firing are the managed gates in the directory git reads.
	Firing []string `json:"firing,omitempty"`
	// Missing are the declared hooks git runs no gate for -- the ones that
	// make this repository's commits ungated.
	Missing []string `json:"missing,omitempty"`
	// State is the verdict.
	State GateState `json:"state"`
}

// BlockingHooks are the hooks that can refuse work: git abandons the commit
// or the push when one exits non-zero. A post-commit hook runs after the
// commit has landed, so a repository whose only hook is post-commit gates
// nothing, however faithfully that hook fires.
var BlockingHooks = []string{"pre-commit", "pre-push"}

// Gated reports whether git runs a gate for every blocking hook the
// repository declares. It is the question a fleet survey exists to answer per
// repository, and the one definition of gated the survey, `doctor` and
// `hooks install --fleet` share.
//
// Only [BlockingHooks] count. A missing post-commit hook costs a
// notification, not a gate, so reporting the repository as ungated over one
// would bury the repositories whose commits really do go unchecked -- and a
// repository that declares no blocking hook has no gate to lose.
func (r RepoGates) Gated() bool {
	for _, name := range r.Missing {
		if slices.Contains(BlockingHooks, name) {
			return false
		}
	}
	return true
}

// Summary states in one line what does not fire and why.
func (r RepoGates) Summary() string {
	switch r.State {
	case GateShadowed:
		return fmt.Sprintf("%s: %s installed but git reads %s (%s core.hooksPath), so nothing fires",
			r.Repo, strings.Join(r.Missing, ", "), r.ActiveDir, r.Scope)
	case GateUninstalled:
		return fmt.Sprintf("%s: pipelines declare %s but no gate is installed",
			r.Repo, strings.Join(r.Missing, ", "))
	default:
		return fmt.Sprintf("%s: %s", r.Repo, r.State)
	}
}

// Remedy is the command that arms the repository.
func (r RepoGates) Remedy() string {
	return fmt.Sprintf("sparkwing pipeline hooks install --repo %s", r.Repo)
}

// Survey classifies one repository. declared names the hooks its pipelines
// ask git to run; an empty list means there is nothing to arm.
//
// The verdict turns on the directory git reads rather than the one the hooks
// were written into. A core.hooksPath override replaces .git/hooks wholesale,
// so a repository can hold a complete set of gates and still run none of
// them -- the failure this survey exists to name, and the reason a survey
// that only listed installed files would report every such repository as
// healthy.
//
// A path with no reachable git checkout reads as GateUndeclared. A registry
// that has outlived a checkout is the operator's to prune, and reporting it
// as an ungated repository would bury the ones that really are.
func Survey(git Git, repoRoot string, declared []string) RepoGates {
	row := RepoGates{Repo: repoRoot, Declared: sortedCopy(declared), State: GateUndeclared}
	hooksDir, err := Dir(repoRoot)
	if err != nil {
		return row
	}
	row.HooksDir = hooksDir
	row.Installed = Gates(hooksDir)

	active, scope := ActivePath(git, repoRoot)
	readDir := hooksDir
	if active != "" && !SameDir(active, hooksDir) {
		row.ActiveDir, row.Scope = active, scope
		readDir = active
	}
	row.Firing = Gates(readDir)

	if len(row.Declared) == 0 {
		return row
	}
	firing := map[string]bool{}
	for _, n := range row.Firing {
		firing[n] = true
	}
	installed := map[string]bool{}
	for _, n := range row.Installed {
		installed[n] = true
	}
	for _, n := range row.Declared {
		if !firing[n] {
			row.Missing = append(row.Missing, n)
		}
	}
	switch {
	case len(row.Missing) == 0:
		row.State = GateArmed
	case allIn(row.Missing, installed):
		row.State = GateShadowed
	default:
		row.State = GateUninstalled
	}
	return row
}

// SurveyFleet classifies every repository in repoRoots, asking declared for
// the hooks each one's pipelines want. Rows come back sorted by path so two
// sweeps of the same machine are comparable.
//
// Taking the roots as an argument keeps the enumeration -- the registry, a
// scan, a test's fixture -- out of the classification, which is what lets the
// same code answer for a machine and for a temp directory.
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

// Ungated returns the surveyed repositories a commit or a push goes through
// unchecked in, dropping the ones that ask for no gate. It is the actionable
// half of a survey.
func Ungated(rows []RepoGates) []RepoGates {
	var out []RepoGates
	for _, r := range rows {
		if !r.Gated() {
			out = append(out, r)
		}
	}
	return out
}

// cacheMachineWideConfig memoizes the --global git lookups a fleet survey
// would otherwise repeat once per repository. The machine's core.hooksPath is
// one value however many checkouts ask for it, and the survey asked git
// separately for each -- on a forty-repo machine that is forty processes
// spawned to read the same line. Repository-scoped lookups are passed
// straight through, since those genuinely differ per repo.
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

func allIn(names []string, set map[string]bool) bool {
	for _, n := range names {
		if !set[n] {
			return false
		}
	}
	return true
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
