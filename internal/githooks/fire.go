package githooks

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SelfTestEnv is the environment variable that, set on a git command, makes
// every sparkwing-managed blocking hook refuse the operation and name its own
// file.
//
// It is a refusal-only switch: the hook can be told to stop a commit, never to
// let one through. A variable that could make a gate pass would be a bypass
// with an environment variable for a key, so the only value it has is the one
// that costs an operator something.
const SelfTestEnv = "SPARKWING_HOOK_SELFTEST"

// selfTestRefusal is the line a hook prints when SelfTestEnv is set, up to the
// hook path it appends. Matching on it is how a driver tells a refusal it
// asked for from a commit that failed for its own reasons.
const selfTestRefusal = "sparkwing hook self-test: refusing this commit; hook="

// SelfTestScript is the guard a managed blocking hook opens with. It exits
// before any pipeline runs, so a driver can make a gate refuse without paying
// for the gate -- and without running a step that might mean something to the
// repository.
//
// The hook path is resolved to an absolute directory rather than echoed as
// git passed it, because that path is the answer: a hook file outside the
// repository being committed to is a gate the repository borrowed, which reads
// identically to an armed one from anywhere else.
func SelfTestScript() string {
	return "if [ -n \"${" + SelfTestEnv + ":-}\" ]; then\n" +
		"\td=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" 2>/dev/null && pwd) || d=$(dirname -- \"$0\")\n" +
		"\techo \"" + selfTestRefusal + "$d/$(basename -- \"$0\")\" >&2\n" +
		"\texit 1\n" +
		"fi\n"
}

// CarriesSelfTest reports whether a hook script can be made to refuse. A
// managed hook written before the guard existed cannot, which is why a driver
// has to ask before concluding anything from a commit that was not refused.
func CarriesSelfTest(script string) bool {
	return strings.Contains(script, SelfTestEnv)
}

// FireVerdict is what a repository's commit path actually did when asked to
// refuse a commit.
type FireVerdict string

const (
	// FireRefused means git refused the commit, using a hook inside the
	// repository being committed to. It is the only verdict that establishes
	// an enforced gate.
	FireRefused FireVerdict = "refused"
	// FireBorrowed means git refused the commit using a hook file outside the
	// repository. The gate is enforced, and it belongs to someone else.
	FireBorrowed FireVerdict = "borrowed"
	// FireAccepted means the commit landed. Whatever the repository declares
	// and whatever sits in its hook directory, git runs nothing here that can
	// refuse work.
	FireAccepted FireVerdict = "accepted"
	// FireUnprovable means git runs a hook this check cannot make refuse: one
	// sparkwing did not write, or a managed one predating the self-test
	// guard. Not a pass -- the question was not answered.
	FireUnprovable FireVerdict = "unprovable"
	// FireInconclusive means the commit was refused by something other than
	// the self-test, so the refusal says nothing about the gate.
	FireInconclusive FireVerdict = "inconclusive"
	// FireUndeclared means no pipeline asks for a hook that can refuse a
	// commit, so there is no gate to fire.
	FireUndeclared FireVerdict = "undeclared"
	// FireError means the check could not be run.
	FireError FireVerdict = "error"
)

// FireResult is one repository's answer, observed rather than inferred.
type FireResult struct {
	// Repo is the checkout root the commit was attempted in.
	Repo string `json:"repo"`
	// Verdict is what happened.
	Verdict FireVerdict `json:"verdict"`
	// Hook is the absolute path of the hook file that refused, as that file
	// reported it. Empty unless something refused.
	Hook string `json:"hook,omitempty"`
	// HeadMoved is set when the repository's HEAD is not the commit it was on
	// before the attempt. It must never be true, and reporting it is cheaper
	// than trusting it.
	HeadMoved bool `json:"head_moved,omitempty"`
	// Detail explains a verdict that is not FireRefused, in the terms an
	// operator acts on.
	Detail string `json:"detail,omitempty"`
}

// Enforced reports whether this repository's own gate was observed refusing a
// commit. Every other verdict, including a borrowed gate that did refuse one,
// leaves the repository unable to claim an enforced gate of its own.
func (r FireResult) Enforced() bool {
	return r.Verdict == FireRefused && !r.HeadMoved
}

// Summary states the observation in one line.
func (r FireResult) Summary() string {
	switch r.Verdict {
	case FireRefused:
		return fmt.Sprintf("%s: the commit was refused by %s", r.Repo, r.Hook)
	case FireBorrowed:
		return fmt.Sprintf("%s: the commit was refused by %s, which is outside this repo", r.Repo, r.Hook)
	default:
		return fmt.Sprintf("%s: %s -- %s", r.Repo, r.Verdict, r.Detail)
	}
}

// Fire attempts a commit in repoRoot that a managed gate must refuse, and
// reports what git did.
//
// This is the only check that separates an armed gate from an armed-looking
// one. Reading a hook directory cannot: a repository whose core.hooksPath
// points at a sibling's hooks holds no gate of its own and refuses commits
// anyway, and one whose hooks are shadowed holds a complete set and refuses
// nothing. Both inspect as something they are not, and the difference is only
// visible in what a commit does.
//
// Nothing about the repository is touched. The attempt runs in a throwaway
// linked worktree, which shares the repository's config -- so the same
// core.hooksPath and the same hooks apply -- while carrying its own index and
// its own detached HEAD, so a commit that is not refused lands on nothing and
// is removed with the worktree.
//
// Only a hook sparkwing wrote and that carries the self-test guard is ever
// executed. Anything else returns FireUnprovable without a commit attempt,
// because running a hand-written hook or a real gate to see what it does would
// mean running arbitrary work in an operator's repository to answer a
// diagnostic question.
func Fire(git Git, repoRoot string, declared []string) FireResult {
	res := FireResult{Repo: repoRoot}
	if !containsBlocking(declared) {
		res.Verdict, res.Detail = FireUndeclared, "no pipeline declares pre-commit, so nothing here can refuse a commit"
		return res
	}
	hooksDir, err := Dir(repoRoot)
	if err != nil {
		res.Verdict, res.Detail = FireError, err.Error()
		return res
	}
	readDir := hooksDir
	if active, _ := ActivePath(git, repoRoot); active != "" {
		readDir = active
	}
	if v, detail := runnableHook(filepath.Join(readDir, "pre-commit")); v != "" {
		res.Verdict, res.Detail = v, detail
		return res
	}
	return attemptRefusedCommit(res, repoRoot, hooksDir)
}

// runnableHook reports the verdict a hook file settles before any commit is
// attempted, or "" when the attempt is worth making. An absent hook is worth
// attempting: the commit lands, which is the observation that a repository
// declaring a gate runs none.
func runnableHook(path string) (FireVerdict, string) {
	body, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", ""
	case err != nil:
		return FireError, err.Error()
	case !strings.Contains(string(body), Marker):
		return FireUnprovable, fmt.Sprintf("git runs %s, which sparkwing did not write, so this check cannot make it refuse without running it", path)
	case !CarriesSelfTest(string(body)):
		return FireUnprovable, fmt.Sprintf("%s predates the self-test guard, so a commit here cannot be refused on demand; re-run `sparkwing pipeline hooks install`", path)
	}
	return "", ""
}

// attemptRefusedCommit stages a file in a throwaway worktree and commits it
// twice: once with the self-test armed, which must be refused, and once with
// hooks switched off, which must land.
//
// The second attempt is the control. A refusal on its own proves only that
// this commit could not be made -- an unrelated failure reads exactly like a
// gate doing its job -- so the check ends by showing that the same staged
// change commits fine when no hook runs. Hooks are switched off for it by
// pointing core.hooksPath at an empty directory, which is why the control
// never executes a gate it might not survive.
func attemptRefusedCommit(res FireResult, repoRoot, hooksDir string) FireResult {
	scratch, err := os.MkdirTemp("", "sparkwing-gate-fire-")
	if err != nil {
		res.Verdict, res.Detail = FireError, err.Error()
		return res
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	worktree := filepath.Join(scratch, "worktree")
	if out, err := runGitIn(repoRoot, nil, "worktree", "add", "--detach", "--no-checkout", worktree); err != nil {
		res.Verdict, res.Detail = FireError, fmt.Sprintf("throwaway worktree: %v: %s", err, out)
		return res
	}
	defer func() {
		_, _ = runGitIn(repoRoot, nil, "worktree", "remove", "--force", worktree)
		_, _ = runGitIn(repoRoot, nil, "worktree", "prune")
	}()

	before, err := runGitIn(worktree, nil, "rev-parse", "HEAD")
	if err != nil {
		res.Verdict, res.Detail = FireError, fmt.Sprintf("read HEAD: %v: %s", err, before)
		return res
	}
	staged := filepath.Join(worktree, "sparkwing-gate-selftest.txt")
	if err := os.WriteFile(staged, []byte("staged by `sparkwing pipeline hooks fire` to see whether the gate refuses it\n"), 0o644); err != nil {
		res.Verdict, res.Detail = FireError, err.Error()
		return res
	}
	if out, err := runGitIn(worktree, nil, "add", "--", staged); err != nil {
		res.Verdict, res.Detail = FireError, fmt.Sprintf("stage: %v: %s", err, out)
		return res
	}

	out, err := runGitIn(worktree, []string{SelfTestEnv + "=refuse"}, "commit", "-m", "sparkwing gate self-test")
	// safety: read HEAD before the control commit, which moves it on purpose.
	after, _ := runGitIn(worktree, nil, "rev-parse", "HEAD")
	res.HeadMoved = strings.TrimSpace(after) != strings.TrimSpace(before)
	if err == nil {
		res.Verdict = FireAccepted
		res.Detail = "the commit landed, so git runs no gate here that can refuse one"
		return res
	}
	hook, refused := refusingHook(out)
	if !refused {
		res.Verdict = FireInconclusive
		res.Detail = fmt.Sprintf("the commit was refused, but not by the self-test, so the refusal says nothing about the gate: %s", lastNonEmptyLine(out))
		return res
	}
	res.Hook = hook
	nohooks := filepath.Join(scratch, "nohooks")
	if err := os.Mkdir(nohooks, 0o755); err != nil {
		res.Verdict, res.Detail = FireError, err.Error()
		return res
	}
	if out, err := runGitIn(worktree, nil, "-c", "core.hooksPath="+nohooks, "commit", "-m", "sparkwing gate self-test control"); err != nil {
		res.Verdict = FireInconclusive
		res.Detail = fmt.Sprintf("the control commit, with hooks switched off, was refused too, so the gate is not what refused the first one: %s", lastNonEmptyLine(out))
		return res
	}
	res.Verdict = FireRefused
	if !SameDir(filepath.Dir(hook), hooksDir) {
		res.Verdict = FireBorrowed
		res.Detail = fmt.Sprintf("the gate that refused lives in %s, not %s, so nothing in this repo declares or keeps it", filepath.Dir(hook), hooksDir)
	}
	return res
}

// refusingHook pulls the hook path out of a self-test refusal.
func refusingHook(out string) (string, bool) {
	_, rest, found := strings.Cut(out, selfTestRefusal)
	if !found {
		return "", false
	}
	path, _, _ := strings.Cut(rest, "\n")
	return strings.TrimSpace(path), true
}

// runGitIn runs git in dir with extra environment entries, returning its
// combined output. The self-test refusal arrives on stderr and git's own
// failure text on stdout, and a verdict needs both.
func runGitIn(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func lastNonEmptyLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return "no output"
}

func containsBlocking(declared []string) bool {
	for _, n := range declared {
		if n == "pre-commit" {
			return true
		}
	}
	return false
}
