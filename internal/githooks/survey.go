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
	// GateBorrowed means git runs a gate for a declared hook out of another
	// repository's hook directory. The commit is refused or accepted by a
	// file nothing here declares, installs, or can keep: an uninstall in the
	// owning repository disarms this one with no change to it.
	GateBorrowed GateState = "borrowed"
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
	// Shadowed are the declared hooks installed in HooksDir that git runs
	// nothing for, because it reads ActiveDir instead.
	Shadowed []string `json:"shadowed,omitempty"`
	// Borrowed are the declared hooks git runs a gate for out of a directory
	// that is not this repository's own.
	Borrowed []string `json:"borrowed,omitempty"`
	// Missing are the declared hooks no managed gate exists for at all,
	// neither where git reads nor in this repository's own hook directory.
	//
	// It is disjoint from Installed, Shadowed and Borrowed. A hook reported
	// as both installed and missing is the report contradicting itself, and
	// the reader who resolves that contradiction the wrong way concludes
	// there is no gate to fix.
	Missing []string `json:"missing,omitempty"`
	// State is the verdict.
	State GateState `json:"state"`
}

// NotFiring are the declared hooks git runs no gate for, shadowed and never
// installed together. It is what a reader wants in one column: both reasons
// leave the hook silent, and only the remedy differs.
func (r RepoGates) NotFiring() []string {
	if len(r.Shadowed) == 0 {
		return r.Missing
	}
	return sortedCopy(append(append([]string(nil), r.Shadowed...), r.Missing...))
}

// BlockingHooks are the hooks that can refuse work: git abandons the commit
// or the push when one exits non-zero. A post-commit hook runs after the
// commit has landed, so a repository whose only hook is post-commit gates
// nothing, however faithfully that hook fires.
var BlockingHooks = []string{"pre-commit", "pre-push"}

// Gated reports whether the repository's own gates run for every blocking
// hook it declares. It is the question a fleet survey exists to answer per
// repository, and the one definition of gated the survey, `doctor` and
// `hooks install --fleet` share.
//
// Only [BlockingHooks] count. A missing post-commit hook costs a
// notification, not a gate, so reporting the repository as ungated over one
// would bury the repositories whose commits really do go unchecked -- and a
// repository that declares no blocking hook has no gate to lose.
//
// A borrowed gate counts as ungated even though it does refuse commits.
// Nothing in this repository declares the file that runs, so an uninstall in
// the repository that owns it disarms this one with no commit here and no
// warning, and whatever that gate checks is the other repository's rules.
// Claiming a gate the repository neither owns nor controls is the same false
// claim as claiming a shadowed one, and it is worse for being invisible.
func (r RepoGates) Gated() bool {
	for _, name := range append(r.NotFiring(), r.Borrowed...) {
		if slices.Contains(BlockingHooks, name) {
			return false
		}
	}
	return true
}

// Summary states in one line what does not fire and why. Every hook the
// repository declares and does not run its own gate for is named, whatever the
// one-word state, so the word never has to carry the whole answer.
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

// Remedy is the command that arms the repository. A repository pointed at
// another one's hooks needs its own override cleared first: install treats a
// repository-scoped core.hooksPath as deliberate and leaves it alone, so on a
// borrowed gate the install alone is a no-op.
func (r RepoGates) Remedy() string {
	if len(r.Borrowed) > 0 {
		return fmt.Sprintf("git -C %s config --unset core.hooksPath, then sparkwing pipeline hooks install --repo %s", r.Repo, r.Repo)
	}
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
//
// Each declared hook lands in exactly one of firing, borrowed, shadowed and
// missing, so no hook is ever reported as both installed and missing. The
// one-word State takes the worst of them, and GateBorrowed is the worst
// because it is the only one whose report and whose behavior disagree: a
// shadowed or uninstalled repository is honest about accepting ungated
// commits, so an operator who reads it acts, while a borrowed gate refuses
// commits under a state word that says no gate is installed.
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
