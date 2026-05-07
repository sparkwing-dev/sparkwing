// Wing-owned flags. parseWingFlags walks args manually (not pflag)
// because the pipeline binary defines its own flags; we strip what
// we know and pass the rest through untouched.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/bincache"
	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
)

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func atoiNonNeg(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("must be >= 0, got %d", n)
	}
	return n, nil
}

type wingFlags struct {
	from      string
	on        string
	config    string
	retryOf   string
	fullRetry bool
	noUpdate  bool
	verbose   bool
	// secrets sources secrets from the named profile's controller.
	// Orthogonal to --on: --secrets prod resolves against prod even
	// when running locally. Empty = laptop dotenv.
	secrets   string
	changeDir string
	// mode: "" / "local" = in-process workers; "ci-embedded" = capped
	// local procs + S3 storage; "distributed" is reached via --on.
	mode string
	// workers caps concurrent nodes in ci-embedded mode; 0 = NumCPU.
	workers int
	// IMP-007: --start-at / --stop-at name an inclusive WorkStep window
	// the orchestrator runs; ids outside the resulting reachability
	// set are skipped with `step_skipped`. Either bound can be empty
	// to leave that side open. Unknown ids fail the run with a
	// "did you mean X?" suggestion at registration time.
	startAt string
	stopAt  string
	// IMP-014: --dry-run runs each step's DryRunFn instead of its
	// apply Fn. No mutation; safe to run from agents and CI gates
	// before destructive operations. Steps without a DryRunFn (and
	// without an explicit SafeWithoutDryRun marker) soft-skip with
	// reason `no_dry_run_defined` so the contract gap is visible.
	dryRun bool
	// IMP-015: per-marker escape hatches for the blast-radius gate.
	// Each authorizes dispatch when the matching marker is declared
	// on any step. --dry-run bypasses all three regardless. The gate
	// degrades gracefully (no marker = no block) so pipelines built
	// before IMP-015 keep working untouched.
	allowDestructive bool
	allowProd        bool
	allowMoney       bool
}

// collectPipelineArgs parses passthrough into TriggerRequest.Args.
// Bare flags map to "true". No schema validation here: the controller
// re-parses against the remote pipeline's own schema.
func collectPipelineArgs(passthrough []string) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(passthrough) {
		tok := passthrough[i]
		if !strings.HasPrefix(tok, "--") {
			i++
			continue
		}
		name := strings.TrimPrefix(tok, "--")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			key := name[:eq]
			if key != "" {
				out[key] = name[eq+1:]
			}
			i++
			continue
		}
		// Following "--" token = next flag; treat current as bool.
		if i+1 < len(passthrough) && !strings.HasPrefix(passthrough[i+1], "--") {
			out[name] = passthrough[i+1]
			i += 2
			continue
		}
		out[name] = "true"
		i++
	}
	return out
}

// wingTokenSpec classifies a wing-owned flag token at parse time.
// takesValue is true for flags that consume the next token (e.g.
// --on prod) versus pure boolean flags (e.g. --full). Used by both
// parseWingFlags and extractPipelineName so the two stay in sync.
type wingTokenSpec struct {
	tok        string
	takesValue bool
}

// wingTokenSpecs is the canonical wing-owned flag-token table. Both
// parseWingFlags and extractPipelineName read from this so adding a
// new wing flag is a one-line change. The list mirrors the SDK's
// ReservedFlagNames() set; tests pin them in lockstep.
var wingTokenSpecs = []wingTokenSpec{
	{tok: "--from", takesValue: true},
	{tok: "--on", takesValue: true},
	{tok: "--retry-of", takesValue: true},
	{tok: "--full", takesValue: false},
	{tok: "--config", takesValue: true},
	{tok: "--no-update", takesValue: false},
	{tok: "--verbose", takesValue: false},
	{tok: "-v", takesValue: false},
	{tok: "--secrets", takesValue: true},
	{tok: "--mode", takesValue: true},
	{tok: "--workers", takesValue: true},
	{tok: "--start-at", takesValue: true},
	{tok: "--stop-at", takesValue: true},
	{tok: "--dry-run", takesValue: false},
	{tok: "--allow-destructive", takesValue: false},
	{tok: "--allow-prod", takesValue: false},
	{tok: "--allow-money", takesValue: false},
	{tok: "-C", takesValue: true},
	{tok: "--change-directory", takesValue: true},
}

// classifyWingFlag returns (spec, ok) for a token that matches a
// wing-owned flag in either bare (`--on`) or `=`-joined
// (`--on=prod`) form. Used by extractPipelineName to know whether
// to skip one or two tokens when scanning for the pipeline-name
// positional.
func classifyWingFlag(tok string) (wingTokenSpec, bool) {
	for _, s := range wingTokenSpecs {
		if tok == s.tok {
			return s, true
		}
		if s.takesValue && strings.HasPrefix(tok, s.tok+"=") {
			// `=`-joined form is one token regardless of takesValue.
			return wingTokenSpec{tok: s.tok, takesValue: false}, true
		}
	}
	return wingTokenSpec{}, false
}

// strictOrderFlags must precede the pipeline-name positional. Other
// wing flags (`--on`, `--from`, ...) are still consumed from any
// position by parseWingFlags so existing `wing build --on prod`
// muscle memory keeps working; the strict-order set is the subset
// where ambiguity vs the positional is the actual bug -- `-C` whose
// long form `--change-directory` is the original IMP-006 repro.
// Adding more flags here is a one-line change and immediately
// covered by the existing test matrix.
var strictOrderFlags = map[string]bool{
	"-C":                 true,
	"--change-directory": true,
}

// isStrictOrderFlagToken returns true when tok is a strict-order
// wing flag (`-C`, `--change-directory`, or their `=value` form).
func isStrictOrderFlagToken(tok string) bool {
	if strictOrderFlags[tok] {
		return true
	}
	if eq := strings.IndexByte(tok, '='); eq >= 0 {
		return strictOrderFlags[tok[:eq]]
	}
	return false
}

// extractPipelineName implements the IMP-006 strict-ordering contract
// for `wing <name>` and `sparkwing run <name>`. The rule is narrow:
// only the strict-order flag set (currently just `-C` /
// `--change-directory`) must appear BEFORE the pipeline-name
// positional. Other wing-owned flags (`--on`, `--from`, `--start-at`,
// ...) are still consumed from any position by parseWingFlags so
// existing `wing build --on prod` invocations keep working untouched.
//
// The first non-flag token (or the token immediately after a literal
// `--`) is the pipeline name; everything else is returned as `rest`
// for downstream parseWingFlags / pipeline-binary handling. Wing
// flag-VALUE pairs are consumed as one unit when scanning so a path
// argument like `/path` after `-C` is never mistaken for the
// positional.
//
// Returns an error if `-C` / `--change-directory` appears after the
// pipeline name. That covers the previously-silent
// `wing run foo -C /path` shape, where `-C` would otherwise be
// treated as a pipeline flag and the path would either get ignored
// or land in the pipeline's own flag bag.
//
// `--` is honored as a strict break: tokens after `--` are treated
// as opaque pipeline args -- no wing-flag detection runs on them, so
// `wing run foo -- --my-pipeline-flag value` parses cleanly even if
// a future wing flag shares the name.
func extractPipelineName(args []string) (name string, rest []string, err error) {
	rest = make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		tok := args[i]
		if tok == "--" {
			// Everything after `--` is pipeline-side; the pipeline
			// name must already have been found, otherwise the
			// invocation has no pipeline at all.
			if name == "" {
				return "", nil, fmt.Errorf("pipeline name required before `--`")
			}
			rest = append(rest, args[i+1:]...)
			return name, rest, nil
		}
		if isStrictOrderFlagToken(tok) {
			if name != "" {
				return "", nil, fmt.Errorf("ambiguous flag position: %s must precede the pipeline name %q", tok, name)
			}
			// Consume the flag (and its value if applicable) into
			// rest so parseWingFlags handles the actual binding.
			spec, ok := classifyWingFlag(tok)
			rest = append(rest, tok)
			if ok && spec.takesValue && i+1 < len(args) {
				rest = append(rest, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}
		if spec, ok := classifyWingFlag(tok); ok {
			// Non-strict-order wing flag (e.g. --on, --from):
			// consume the value alongside it so it can't be
			// mistaken for the pipeline-name positional, then keep
			// scanning. parseWingFlags will bind it later.
			rest = append(rest, tok)
			if spec.takesValue && i+1 < len(args) {
				rest = append(rest, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}
		// Non-wing-flag token. The first one that doesn't start with
		// `-` (and isn't a value of a wing flag) is the pipeline
		// name. Flag-looking tokens are pipeline flags -- pass
		// through to the binary on either side of the positional.
		if name == "" && (tok == "" || tok[0] != '-') {
			name = tok
			i++
			continue
		}
		rest = append(rest, tok)
		i++
	}
	if name == "" {
		return "", nil, fmt.Errorf("pipeline name required")
	}
	return name, rest, nil
}

// parseWingFlags splits wing-owned flags from pass-through args.
// Unknown / malformed-trailing flags fall through to the pipeline binary.
func parseWingFlags(args []string) (wingFlags, []string) {
	var wf wingFlags
	pass := make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--from":
			if i+1 < len(args) {
				wf.from = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--from="):
			wf.from = strings.TrimPrefix(a, "--from=")
			i++
		case a == "--on":
			if i+1 < len(args) {
				wf.on = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--on="):
			wf.on = strings.TrimPrefix(a, "--on=")
			i++
		case a == "--retry-of":
			if i+1 < len(args) {
				wf.retryOf = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--retry-of="):
			wf.retryOf = strings.TrimPrefix(a, "--retry-of=")
			i++
		case a == "--full":
			wf.fullRetry = true
			i++
		case a == "--config":
			if i+1 < len(args) {
				wf.config = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--config="):
			wf.config = strings.TrimPrefix(a, "--config=")
			i++
		case a == "--no-update":
			wf.noUpdate = true
			i++
		case a == "--verbose", a == "-v":
			wf.verbose = true
			i++
		case a == "--secrets":
			if i+1 < len(args) {
				wf.secrets = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--secrets="):
			wf.secrets = strings.TrimPrefix(a, "--secrets=")
			i++
		case a == "--mode":
			if i+1 < len(args) {
				wf.mode = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--mode="):
			wf.mode = strings.TrimPrefix(a, "--mode=")
			i++
		case a == "--workers":
			if i+1 < len(args) {
				if n, err := atoiNonNeg(args[i+1]); err == nil {
					wf.workers = n
					i += 2
					continue
				}
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--workers="):
			if n, err := atoiNonNeg(strings.TrimPrefix(a, "--workers=")); err == nil {
				wf.workers = n
			}
			i++
		case a == "--start-at":
			if i+1 < len(args) {
				wf.startAt = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--start-at="):
			wf.startAt = strings.TrimPrefix(a, "--start-at=")
			i++
		case a == "--stop-at":
			if i+1 < len(args) {
				wf.stopAt = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--stop-at="):
			wf.stopAt = strings.TrimPrefix(a, "--stop-at=")
			i++
		case a == "--dry-run", a == "--dry-run=true":
			wf.dryRun = true
			i++
		case a == "--dry-run=false":
			wf.dryRun = false
			i++
		case a == "--allow-destructive", a == "--allow-destructive=true":
			wf.allowDestructive = true
			i++
		case a == "--allow-destructive=false":
			wf.allowDestructive = false
			i++
		case a == "--allow-prod", a == "--allow-prod=true":
			wf.allowProd = true
			i++
		case a == "--allow-prod=false":
			wf.allowProd = false
			i++
		case a == "--allow-money", a == "--allow-money=true":
			wf.allowMoney = true
			i++
		case a == "--allow-money=false":
			wf.allowMoney = false
			i++
		case a == "-C", a == "--change-directory":
			if i+1 < len(args) {
				wf.changeDir = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--change-directory="):
			wf.changeDir = strings.TrimPrefix(a, "--change-directory=")
			i++
		default:
			pass = append(pass, a)
			i++
		}
	}
	return wf, pass
}

// setupFromRef creates a git worktree at ref. Caller must defer cleanup.
// Best-effort fetch first so unseen refs resolve; fetch failure is non-fatal.
func setupFromRef(sparkwingDir, ref string) (worktreeDir string, sparkwingSub string, cleanup func(), err error) {
	repoRoot := filepath.Dir(sparkwingDir)

	tmpDir, err := os.MkdirTemp("", "sparkwing-from-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("mkdir tmp: %w", err)
	}

	_ = exec.Command("git", "-C", repoRoot, "fetch", "--quiet", "origin", ref).Run()

	out, err := exec.Command("git", "-C", repoRoot,
		"worktree", "add", "--detach", "--quiet", tmpDir, ref).CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", "", nil, fmt.Errorf("git worktree add %s: %w: %s",
			ref, err, strings.TrimSpace(string(out)))
	}

	sub := filepath.Join(tmpDir, ".sparkwing")
	if fi, statErr := os.Stat(sub); statErr != nil || !fi.IsDir() {
		_ = exec.Command("git", "-C", repoRoot,
			"worktree", "remove", "--force", tmpDir).Run()
		_ = os.RemoveAll(tmpDir)
		return "", "", nil, fmt.Errorf("ref %s has no .sparkwing/ directory", ref)
	}

	cleanup = func() {
		// worktree remove (not just RemoveAll) so .git/worktrees stays clean.
		_ = exec.Command("git", "-C", repoRoot,
			"worktree", "remove", "--force", tmpDir).Run()
		_ = os.RemoveAll(tmpDir)
	}
	return tmpDir, sub, cleanup, nil
}

// dispatchRemote POSTs a TriggerRequest to the profile's controller.
// Does NOT compile locally — assumes the remote already has the pipeline.
func dispatchRemote(pipelineName string, wf wingFlags, passthrough []string) error {
	prof, err := resolveProfile(wf.on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "wing --on"); err != nil {
		return err
	}

	args := collectPipelineArgs(passthrough)
	source := "wing"
	if host, err := os.Hostname(); err == nil && host != "" {
		source = "wing@" + host
	}
	var userName string
	if u, err := user.Current(); err == nil {
		userName = u.Username
	}

	// Plumb repo via both env (warm-runner reads for clone+compile) and
	// Git meta (dashboard reads for the repo pill).
	branch, sha, repoSlug, repoURL := detectRemoteGit()
	envMap := map[string]string{}
	if repoSlug != "" {
		envMap["GITHUB_REPOSITORY"] = repoSlug
	}
	// IMP-007: --start-at / --stop-at are wing-level on the local CLI;
	// the remote runner reads them as SPARKWING_START_AT /
	// SPARKWING_STOP_AT from trigger env. Same env-var protocol the
	// laptop-local exec path uses, so behavior is identical across
	// venues.
	if wf.startAt != "" {
		envMap["SPARKWING_START_AT"] = wf.startAt
	}
	if wf.stopAt != "" {
		envMap["SPARKWING_STOP_AT"] = wf.stopAt
	}
	// IMP-014: forward --dry-run to the remote runner via the same
	// env-var protocol the local-exec path uses. Behavior is
	// identical across venues so `wing X --on prod --dry-run`
	// previews the same way it does locally.
	if wf.dryRun {
		envMap["SPARKWING_DRY_RUN"] = "1"
	}

	triggerBranch := wf.from
	if triggerBranch == "" {
		triggerBranch = branch
	}

	owner, name := "", ""
	if slash := strings.IndexByte(repoSlug, '/'); slash > 0 {
		owner, name = repoSlug[:slash], repoSlug[slash+1:]
	}

	req := client.TriggerRequest{
		Pipeline: pipelineName,
		Args:     args,
		Trigger: client.TriggerMeta{
			Source: source,
			User:   userName,
			Env:    envMap,
		},
		Git: client.GitMeta{
			Branch:      triggerBranch,
			SHA:         sha,
			Repo:        name,
			RepoURL:     repoURL,
			GithubOwner: owner,
			GithubRepo:  name,
		},
		RetryOf: wf.retryOf,
	}
	// --full is local-only; warn loudly when it would silently no-op remotely.
	if wf.fullRetry && wf.retryOf != "" {
		fmt.Fprintln(os.Stderr, "wing: --full is local-only; remote retry always skips passed nodes (ignoring --full)")
	}

	// IMP-005: best-effort eager refresh closes the
	// `git push && wing X --on prod` race where the gitcache
	// hasn't yet mirrored the just-pushed SHA. The retry in the
	// runner's trigger loop catches the residual race; this just
	// shrinks the window to ~zero on the happy path. 5s ceiling so
	// a wedged or unreachable cache never blocks dispatch — log a
	// warning and continue.
	if prof.Gitcache != "" && repoURL != "" {
		refreshCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := bincache.RefreshRepo(refreshCtx, prof.Gitcache, repoURL); err != nil {
			fmt.Fprintf(os.Stderr,
				"wing: gitcache eager refresh failed (%v); proceeding — runner will retry on stale-SHA\n",
				err)
		}
		cancel()
	}

	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	resp, err := c.CreateTrigger(context.Background(), req)
	if err != nil {
		return fmt.Errorf("create trigger on %s: %w", prof.Name, err)
	}

	fmt.Fprintf(os.Stdout, "dispatched %s on %s as %s (status=%s)\n",
		pipelineName, prof.Name, resp.RunID, resp.Status)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "tail logs:\n")
	fmt.Fprintf(os.Stderr, "  sparkwing runs logs --run %s --on %s --follow\n",
		resp.RunID, prof.Name)
	fmt.Fprintf(os.Stderr, "check status:\n")
	fmt.Fprintf(os.Stderr, "  sparkwing runs status --run %s --on %s\n",
		resp.RunID, prof.Name)
	return nil
}

// detectRemoteGit reads cwd's git state. Unresolved fields return empty.
func detectRemoteGit() (branch, sha, repo, repoURL string) {
	if out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		branch = strings.TrimSpace(string(out))
		if branch == "HEAD" {
			branch = ""
		}
	}
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		sha = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "remote", "get-url", "origin").Output(); err == nil {
		repoURL = strings.TrimSpace(string(out))
		repo = parseGithubOwnerRepo(repoURL)
	}
	return branch, sha, repo, repoURL
}

// parseGithubOwnerRepo extracts "owner/name" from github SSH/HTTPS URLs;
// empty for non-github hosts so warm-runner doesn't attempt unknown clones.
func parseGithubOwnerRepo(url string) string {
	if strings.HasPrefix(url, "git@github.com:") {
		rest := strings.TrimPrefix(url, "git@github.com:")
		rest = strings.TrimSuffix(rest, ".git")
		return rest
	}
	for _, prefix := range []string{"https://github.com/", "http://github.com/"} {
		if strings.HasPrefix(url, prefix) {
			rest := strings.TrimPrefix(url, prefix)
			rest = strings.TrimSuffix(rest, ".git")
			return rest
		}
	}
	return ""
}
