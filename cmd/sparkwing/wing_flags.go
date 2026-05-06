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
