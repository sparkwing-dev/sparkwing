// sparkwing run flag parsing. parseRunFlags walks args manually (not
// pflag) because the pipeline binary defines its own flags; we strip
// the sw-prefixed flags we know and pass the rest through untouched.
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

	"github.com/sparkwing-dev/sparkwing/bincache"
	"github.com/sparkwing-dev/sparkwing/controller/client"
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

type runFlags struct {
	from      string
	on        string
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
	// --start-at / --stop-at name an inclusive WorkStep window the
	// orchestrator runs; ids outside the resulting reachability set
	// are skipped with `step_skipped`. Either bound can be empty to
	// leave that side open. Unknown ids fail the run with a "did you
	// mean X?" suggestion at registration time.
	startAt string
	stopAt  string
	// --dry-run runs each step's DryRunFn instead of its apply Fn.
	// No mutation; safe to run from agents and CI gates before
	// destructive operations. Steps without a DryRunFn (and without
	// an explicit SafeWithoutDryRun marker) soft-skip with reason
	// `no_dry_run_defined` so the contract gap is visible.
	dryRun bool
	// Per-marker escape hatches for the blast-radius gate. Each
	// authorizes dispatch when the matching marker is declared on
	// any step. --dry-run bypasses all three regardless. The gate
	// degrades gracefully (no marker = no block) so pipelines that
	// predate the gate keep working untouched.
	allowDestructive bool
	allowProd        bool
	allowMoney       bool
	// forTarget picks the pipelines.yaml target the run resolves
	// against. Empty means "use the single declared target if there's
	// one, else no target."
	forTarget string
	// jobOverrides forces specific plan-node ids onto specific
	// runners. Each entry is "<jobID>=<runnerName>". Repeated --job
	// for the same id is rejected by the inner orchestrator.
	jobOverrides []string
	// preferLabels biases runner preferences across the run. Each
	// entry is one label term (comma-OR within); --prefer composes
	// with each job's own Prefers (job-level wins on tie).
	preferLabels []string
	// backendsEnv forces a specific environments: entry from
	// backends.yaml, skipping auto-detect. Validated against the
	// resolved file at run start.
	backendsEnv string
	// backendsConfig points at a (possibly synthesized) backends.yaml
	// fragment the inner binary layers underneath defaults. The
	// outer CLI uses this to forward profile-derived storage
	// settings to the child via a temp file.
	backendsConfig string
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

// parseRunFlags splits sparkwing-owned (sw-prefixed) flags from
// pass-through args. Unknown / malformed-trailing flags fall through
// to the pipeline binary.
func parseRunFlags(args []string) (runFlags, []string) {
	var wf runFlags
	pass := make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--sw-from":
			if i+1 < len(args) {
				wf.from = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-from="):
			wf.from = strings.TrimPrefix(a, "--sw-from=")
			i++
		case a == "--sw-on":
			if i+1 < len(args) {
				wf.on = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-on="):
			wf.on = strings.TrimPrefix(a, "--sw-on=")
			i++
		case a == "--sw-retry-of":
			if i+1 < len(args) {
				wf.retryOf = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-retry-of="):
			wf.retryOf = strings.TrimPrefix(a, "--sw-retry-of=")
			i++
		case a == "--sw-full":
			wf.fullRetry = true
			i++
		case a == "--sw-no-update":
			wf.noUpdate = true
			i++
		case a == "--sw-verbose", a == "-v":
			wf.verbose = true
			i++
		case a == "--sw-secrets":
			if i+1 < len(args) {
				wf.secrets = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-secrets="):
			wf.secrets = strings.TrimPrefix(a, "--sw-secrets=")
			i++
		case a == "--sw-mode":
			if i+1 < len(args) {
				wf.mode = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-mode="):
			wf.mode = strings.TrimPrefix(a, "--sw-mode=")
			i++
		case a == "--sw-workers":
			if i+1 < len(args) {
				if n, err := atoiNonNeg(args[i+1]); err == nil {
					wf.workers = n
					i += 2
					continue
				}
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-workers="):
			if n, err := atoiNonNeg(strings.TrimPrefix(a, "--sw-workers=")); err == nil {
				wf.workers = n
			}
			i++
		case a == "--sw-start-at":
			if i+1 < len(args) {
				wf.startAt = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-start-at="):
			wf.startAt = strings.TrimPrefix(a, "--sw-start-at=")
			i++
		case a == "--sw-stop-at":
			if i+1 < len(args) {
				wf.stopAt = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-stop-at="):
			wf.stopAt = strings.TrimPrefix(a, "--sw-stop-at=")
			i++
		case a == "--sw-dry-run", a == "--dry-run=true":
			wf.dryRun = true
			i++
		case a == "--dry-run=false":
			wf.dryRun = false
			i++
		case a == "--sw-allow-destructive", a == "--allow-destructive=true":
			wf.allowDestructive = true
			i++
		case a == "--allow-destructive=false":
			wf.allowDestructive = false
			i++
		case a == "--sw-allow-prod", a == "--allow-prod=true":
			wf.allowProd = true
			i++
		case a == "--allow-prod=false":
			wf.allowProd = false
			i++
		case a == "--sw-allow-money", a == "--allow-money=true":
			wf.allowMoney = true
			i++
		case a == "--allow-money=false":
			wf.allowMoney = false
			i++
		case a == "-C", a == "--sw-change-directory":
			if i+1 < len(args) {
				wf.changeDir = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-change-directory="):
			wf.changeDir = strings.TrimPrefix(a, "--sw-change-directory=")
			i++
		case a == "--sw-for":
			if i+1 < len(args) {
				wf.forTarget = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-for="):
			wf.forTarget = strings.TrimPrefix(a, "--sw-for=")
			i++
		case a == "--sw-job":
			if i+1 < len(args) {
				wf.jobOverrides = append(wf.jobOverrides, args[i+1])
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-job="):
			wf.jobOverrides = append(wf.jobOverrides, strings.TrimPrefix(a, "--sw-job="))
			i++
		case a == "--sw-prefer":
			if i+1 < len(args) {
				wf.preferLabels = append(wf.preferLabels, args[i+1])
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-prefer="):
			wf.preferLabels = append(wf.preferLabels, strings.TrimPrefix(a, "--sw-prefer="))
			i++
		case a == "--sw-backends-env":
			if i+1 < len(args) {
				wf.backendsEnv = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-backends-env="):
			wf.backendsEnv = strings.TrimPrefix(a, "--sw-backends-env=")
			i++
		case a == "--sw-backends-config":
			if i+1 < len(args) {
				wf.backendsConfig = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-backends-config="):
			wf.backendsConfig = strings.TrimPrefix(a, "--sw-backends-config=")
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
func dispatchRemote(pipelineName string, wf runFlags, passthrough []string) error {
	prof, err := resolveProfile(wf.on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "sparkwing run --on"); err != nil {
		return err
	}

	args := collectPipelineArgs(passthrough)
	source := "sparkwing"
	if host, err := os.Hostname(); err == nil && host != "" {
		source = "sparkwing@" + host
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
	// --start-at / --stop-at are sparkwing-level on the local CLI; the
	// remote runner reads them as SPARKWING_START_AT /
	// SPARKWING_STOP_AT from trigger env. Same env-var protocol the
	// laptop-local exec path uses, so behavior is identical across
	// venues.
	if wf.startAt != "" {
		envMap["SPARKWING_START_AT"] = wf.startAt
	}
	if wf.stopAt != "" {
		envMap["SPARKWING_STOP_AT"] = wf.stopAt
	}
	// Forward --dry-run to the remote runner via the same env-var
	// protocol the local-exec path uses. Behavior is identical
	// across venues so `sparkwing run X --on prod --dry-run` previews the
	// same way it does locally.
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
		fmt.Fprintln(os.Stderr, "sparkwing run: --full is local-only; remote retry always skips passed nodes (ignoring --full)")
	}

	// Best-effort eager refresh closes the
	// `git push && sparkwing run X --on prod` race where the gitcache
	// hasn't yet mirrored the just-pushed SHA. The retry in the
	// runner's trigger loop catches the residual race; this just
	// shrinks the window to ~zero on the happy path. 5s ceiling so
	// a wedged or unreachable cache never blocks dispatch — log a
	// warning and continue.
	if prof.Gitcache != "" && repoURL != "" {
		refreshCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := bincache.RefreshRepo(refreshCtx, prof.Gitcache, repoURL); err != nil {
			fmt.Fprintf(os.Stderr,
				"sparkwing run: gitcache eager refresh failed (%v); proceeding — runner will retry on stale-SHA\n",
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
