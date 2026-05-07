// `sparkwing push` -- ship the current repo's HEAD commit to the
// gitcache as a timestamped branch so a remote runner can clone it
// without waiting for a push to GitHub. Companion to
// `sparkwing run --on <profile> --from <ref>`: push first, then
// trigger against the ref that push reports.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/profile"
)

// pushShortCommitLen is how many hex chars we echo back in the
// friendly output. 12 is enough to be unambiguous across any repo
// we'd realistically push from.
const pushShortCommitLen = 12

// pushOutput describes what `push` wrote to gitcache. Returned as a
// struct so we can emit --json in a later iteration without rewriting
// the command.
type pushOutput struct {
	Ref          string `json:"ref"`
	RepoName     string `json:"repo_name"`
	Commit       string `json:"commit"`
	InClusterURL string `json:"in_cluster_url"`
	GitcacheURL  string `json:"gitcache_url"`
}

func runPush(args []string) error {
	fs := flag.NewFlagSet(cmdPush.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	name := fs.String("name", "",
		"repo name registered with gitcache (default: basename of repo root)")
	if err := parseAndCheck(cmdPush, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}

	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if prof.Gitcache == "" {
		return fmt.Errorf("push: profile %q has no gitcache URL (set one via `sparkwing profiles set --name %s --gitcache URL`)",
			prof.Name, prof.Name)
	}

	repoRoot, err := runGit("", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("push: not a git repo: %w", err)
	}
	repoRoot = strings.TrimSpace(repoRoot)

	repoName := *name
	if repoName == "" {
		repoName = strings.TrimSuffix(filepath.Base(repoRoot), ".git")
	}

	commit, err := runGit(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("push: no HEAD commit: %w", err)
	}
	commit = strings.TrimSpace(commit)

	// origin URL feeds gitcache's register endpoint as the canonical
	// name. Missing origin (fork-of-nothing repo) is fine; we fall
	// back to a local: pseudo-URL so gitcache can still track it.
	originURL, _ := runGit(repoRoot, "remote", "get-url", "origin")
	originURL = strings.TrimSpace(originURL)
	if originURL == "" {
		originURL = "local:" + repoName
	}

	fmt.Fprintf(os.Stderr, "registering %s with gitcache\n", repoName)
	if err := pushRegisterRepo(prof, repoName, originURL); err != nil {
		return fmt.Errorf("register repo: %w", err)
	}

	// Timestamped ref so concurrent pushes don't stomp each other.
	// UTC + colon-free so the ref name survives git's ref-name rules.
	ref := "local-" + time.Now().UTC().Format("2006-01-02T15-04-05Z")
	pushURL := pushAuthURL(prof, repoName)
	safeURL := strings.TrimRight(prof.Gitcache, "/") + "/git/" + repoName
	fmt.Fprintf(os.Stderr, "pushing HEAD -> %s:refs/heads/%s\n", safeURL, ref)

	cmd := exec.Command("git", "push", pushURL, "HEAD:refs/heads/"+ref)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git push: %w", err)
	}

	short := commit
	if len(short) > pushShortCommitLen {
		short = short[:pushShortCommitLen]
	}
	inCluster := "http://sparkwing-cache.sparkwing.svc.cluster.local/git/" + repoName
	out := pushOutput{
		Ref:          ref,
		RepoName:     repoName,
		Commit:       short,
		InClusterURL: inCluster,
		GitcacheURL:  safeURL,
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "pushed %s (%s)\n", out.Commit, out.Ref)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "trigger a pipeline with:")
	fmt.Fprintf(os.Stderr, "  sparkwing run --pipeline <name> --on %s --from %s\n", prof.Name, out.Ref)
	// Machine-readable line to stdout in case operators script around
	// push. Keep it stable -- "ref=<ref>" is the contract.
	fmt.Printf("ref=%s repo=%s commit=%s\n", out.Ref, out.RepoName, out.Commit)
	return nil
}

// pushAuthURL embeds the profile's bearer into the git push URL as
// HTTP basic auth so `git push` authenticates without needing a
// `git config credential.helper` dance. Gitcache accepts any
// non-empty username; we use "x-access-token" to mimic GitHub's
// convention and make it obvious in gitcache logs.
func pushAuthURL(prof *profile.Profile, repoName string) string {
	base := strings.TrimRight(prof.Gitcache, "/")
	if prof.Token == "" {
		return base + "/git/" + repoName
	}
	u, err := neturl.Parse(base)
	if err != nil {
		return base + "/git/" + repoName
	}
	u.User = neturl.UserPassword("x-access-token", prof.Token)
	u.Path = strings.TrimRight(u.Path, "/") + "/git/" + repoName
	return u.String()
}

// pushRegisterRepo POSTs /git/register on the gitcache. Idempotent:
// gitcache returns 200 for "already registered, canonical URL
// unchanged" and only errors on name conflicts.
func pushRegisterRepo(prof *profile.Profile, name, repoURL string) error {
	base := strings.TrimRight(prof.Gitcache, "/")
	q := neturl.Values{}
	q.Set("name", name)
	q.Set("repo", repoURL)
	req, err := http.NewRequest(http.MethodPost, base+"/git/register?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if prof.Token != "" {
		req.Header.Set("Authorization", "Bearer "+prof.Token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("register %s: %d %s",
			name, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	// Drain any response body so the connection stays keepalive-eligible.
	// The /register payload is a small JSON we don't use today; reading
	// it makes the body-close cheap.
	var discard bytes.Buffer
	_, _ = discard.Write(body)
	// Try to pull a clean message out of the JSON for operator-facing
	// logs; fall back to nothing if the body isn't JSON.
	var msg struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &msg); err == nil && msg.Message != "" {
		fmt.Fprintln(os.Stderr, "  ", msg.Message)
	}
	return nil
}

// runGit executes a git subcommand inside dir (or cwd when empty)
// and returns stdout. stderr is surfaced into the returned error so
// callers get the actual git message, not a bare exit code.
func runGit(dir string, gitArgs ...string) (string, error) {
	cmd := exec.Command("git", gitArgs...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s",
			strings.Join(gitArgs, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
