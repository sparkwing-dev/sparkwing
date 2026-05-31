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

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

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
	ref string
	// profile names a storage profile from
	// ~/.config/sparkwing/profiles.yaml for local execution: state,
	// logs, and cache route through the profile (with a local SQLite
	// mirror for non-local profiles). Parsed from the flat --profile
	// flag, forwarded to the inner binary as SPARKWING_PROFILE.
	profile  string
	noUpdate bool
	verbose  bool
	// secrets sources secrets from the named profile's controller.
	// Orthogonal to --profile: --secrets prod resolves against prod even
	// when running locally. Empty = laptop dotenv.
	secrets   string
	changeDir string
	// mode: "" / "local" = in-process workers; "ci-embedded" = capped
	// local procs + S3 storage.
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
	// --only is a job-level filter (path.Match glob over JobNode IDs).
	// Matched jobs run; jobs reachable as transitive Needs() ancestors
	// of matched jobs also run (so a glob hitting only the leaves still
	// produces a self-consistent dispatch). Everything else is skipped
	// with `node_skipped`. Mutually exclusive with --start-at / --stop-at:
	// they're a different filter mode (step-level reachability) and
	// intersecting the two would produce surprising selections.
	only string
	// --no-cache disables cache READS for this run; per-node cache
	// WRITES still happen on success so subsequent runs over the same
	// content hit cache normally. Distinct from SPARKWING_NO_BINCACHE
	// (which gates the bincache compiled-pipeline-binary cache).
	noCache bool
	// --dry-run runs each step's DryRunFn instead of its apply Fn.
	// No mutation; safe to run from agents and CI gates before
	// destructive operations. Steps without a DryRunFn (and without
	// an explicit SafeWithoutDryRun marker) soft-skip with reason
	// `no_dry_run_defined` so the contract gap is visible.
	dryRun bool
	// allow is the union of risk labels the operator authorizes via
	// --sw-allow (repeatable; comma-separated allowed). The gate
	// walks the plan's declared labels, subtracts this set, and
	// refuses dispatch if any remain. --sw-dry-run bypasses
	// regardless. The gate degrades gracefully (no labels declared =
	// no block).
	allow []string
	// localOnly forces SQLite state + filesystem logs + filesystem
	// cache for this run, ignoring any configured shared backends.
	// The escape hatch when shared state is misbehaving (stale
	// Postgres, unreachable controller, broken bucket policy) and the
	// operator wants to run against the laptop only.
	localOnly bool
	// boxSlots overrides the host-local concurrency semaphore's cap.
	// String (not int) so empty means "fall back to the heuristic"
	// (which defaults to 0 / disabled). Explicit values (1, 2, ...)
	// enable the semaphore with that cap. Forwarded to the inner
	// pipeline binary as SPARKWING_BOX_SLOTS.
	boxSlots string
	// boxNoWait flips the box-slot semaphore from queueing to
	// fail-fast. CI runners that would rather decline overlap than
	// block enable this. Forwarded as SPARKWING_BOX_NO_WAIT.
	boxNoWait bool
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

// appendCSV splits a comma-separated value and appends non-empty
// entries to out. Used by repeatable flags that also accept
// comma-separated lists (pflag StringSlice semantics).
func appendCSV(out []string, v string) []string {
	for _, part := range strings.Split(v, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		out = append(out, p)
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
		case a == "--sw-ref":
			if i+1 < len(args) {
				wf.ref = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-ref="):
			wf.ref = strings.TrimPrefix(a, "--sw-ref=")
			i++
		case a == "--profile":
			if i+1 < len(args) {
				wf.profile = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--profile="):
			wf.profile = strings.TrimPrefix(a, "--profile=")
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
		case a == "--sw-only":
			if i+1 < len(args) {
				wf.only = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-only="):
			wf.only = strings.TrimPrefix(a, "--sw-only=")
			i++
		case a == "--sw-no-cache":
			wf.noCache = true
			i++
		case a == "--sw-local-only":
			wf.localOnly = true
			i++
		case a == "--sw-dry-run", a == "--dry-run=true":
			wf.dryRun = true
			i++
		case a == "--dry-run=false":
			wf.dryRun = false
			i++
		case a == "--sw-allow":
			if i+1 < len(args) {
				wf.allow = appendCSV(wf.allow, args[i+1])
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-allow="):
			wf.allow = appendCSV(wf.allow, strings.TrimPrefix(a, "--sw-allow="))
			i++
		case a == "-C", a == "--sw-cd":
			if i+1 < len(args) {
				wf.changeDir = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-cd="):
			wf.changeDir = strings.TrimPrefix(a, "--sw-cd=")
			i++
		case a == "--sw-box-slots":
			if i+1 < len(args) {
				wf.boxSlots = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-box-slots="):
			wf.boxSlots = strings.TrimPrefix(a, "--sw-box-slots=")
			i++
		case a == "--sw-no-wait":
			wf.boxNoWait = true
			i++
		default:
			pass = append(pass, a)
			i++
		}
	}
	return wf, pass
}

// setupRefWorktree creates a git worktree at ref. Caller must defer cleanup.
// Best-effort fetch first so unseen refs resolve; fetch failure is non-fatal.
func setupRefWorktree(sparkwingDir, ref string) (worktreeDir, sparkwingSub string, cleanup func(), err error) {
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

// triggerSource builds the trigger_source string a remote dispatch
// records, tagging the originating verb so runs are distinguishable in
// `runs list`: "pipeline-trigger@host" for `pipeline trigger`. Falls
// back to the bare prefix when the hostname can't be read.
func triggerSource(prefix string) string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return prefix + "@" + host
	}
	return prefix
}

// createRemoteTrigger builds and POSTs a TriggerRequest to prof's
// controller, returning the controller's response. It backs `sparkwing
// pipeline trigger`. It does NOT print or tail -- the caller decides how
// to report. prof must already carry a controller.
func createRemoteTrigger(prof *profile.Profile, pipelineName, source string, wf runFlags, passthrough []string) (*client.TriggerResponse, error) {
	args := collectPipelineArgs(passthrough)
	var userName string
	if u, err := user.Current(); err == nil {
		userName = u.Username
	}

	// Plumb repo via both env (warm-runner reads for clone+compile) and
	// Git meta (dashboard reads for the repo pill).
	branch, sha, repoSlug, repoURL := detectRemoteGit()
	if repoSlug == "" {
		return nil, fmt.Errorf("pipeline trigger %q: no github repository detected from cwd. "+
			"The cluster runner needs GITHUB_REPOSITORY to clone the pipeline source. "+
			"Run from inside a checkout of a github repo, or pass --repo OWNER/NAME explicitly.", pipelineName)
	}
	envMap := map[string]string{
		"GITHUB_REPOSITORY": repoSlug,
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
	// across venues so `sparkwing pipeline trigger X --profile prod --dry-run`
	// previews the same way a local run does.
	if wf.dryRun {
		envMap["SPARKWING_DRY_RUN"] = "1"
	}
	if wf.only != "" {
		envMap["SPARKWING_ONLY"] = wf.only
	}
	if wf.noCache {
		envMap["SPARKWING_NO_CACHE"] = "1"
	}

	triggerBranch := wf.ref
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
	}

	// Best-effort eager refresh closes the
	// `git push && sparkwing pipeline trigger X --profile prod` race where
	// the gitcache hasn't yet mirrored the just-pushed SHA. The retry in the
	// runner's trigger loop catches the residual race; this just
	// shrinks the window to ~zero on the happy path. 5s ceiling so a
	// wedged or unreachable cache never blocks dispatch.
	if repoURL != "" {
		discoverCtx, dCancel := context.WithTimeout(context.Background(), 5*time.Second)
		services, derr := discovery.ServicesFor(discoverCtx, prof.ControllerURL(), prof.ControllerToken())
		dCancel()
		if derr == nil && services.CachePod != "" {
			refreshCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := bincache.RefreshRepo(refreshCtx, services.CachePod, repoURL); err != nil {
				fmt.Fprintf(os.Stderr,
					"sparkwing run: gitcache eager refresh failed (%v); proceeding -- runner will retry on stale-SHA\n",
					err)
			}
			cancel()
		}
	}

	c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
	resp, err := c.CreateTrigger(context.Background(), req)
	if err != nil {
		return nil, fmt.Errorf("create trigger on %s: %w", prof.Name, err)
	}
	return resp, nil
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
