package githooks

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const SelfTestEnv = "SPARKWING_HOOK_SELFTEST"

const selfTestRefusal = "sparkwing hook self-test: refusing this commit; hook="

func SelfTestScript() string {
	return "if [ -n \"${" + SelfTestEnv + ":-}\" ]; then\n" +
		"\td=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" 2>/dev/null && pwd) || d=$(dirname -- \"$0\")\n" +
		"\techo \"" + selfTestRefusal + "$d/$(basename -- \"$0\")\" >&2\n" +
		"\texit 1\n" +
		"fi\n"
}

func CarriesSelfTest(script string) bool {
	return strings.Contains(script, SelfTestEnv)
}

type FireVerdict string

const (
	FireRefused FireVerdict = "refused"

	FireBorrowed FireVerdict = "borrowed"

	FireAccepted FireVerdict = "accepted"

	FireUnprovable FireVerdict = "unprovable"

	FireInconclusive FireVerdict = "inconclusive"

	FireUndeclared FireVerdict = "undeclared"

	FireError FireVerdict = "error"
)

type FireResult struct {
	Repo string `json:"repo"`

	Verdict FireVerdict `json:"verdict"`

	Hook string `json:"hook,omitempty"`

	HeadMoved bool `json:"head_moved,omitempty"`

	Detail string `json:"detail,omitempty"`
}

func (r FireResult) Enforced() bool {
	return r.Verdict == FireRefused && !r.HeadMoved
}

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

func refusingHook(out string) (string, bool) {
	_, rest, found := strings.Cut(out, selfTestRefusal)
	if !found {
		return "", false
	}
	path, _, _ := strings.Cut(rest, "\n")
	return strings.TrimSpace(path), true
}

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
