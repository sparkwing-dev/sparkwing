// Command agentreview is the judgment layer of the pre-push gate. It runs
// a roster of specialized reviewers (each a headless claude session) over
// the diff that is about to be pushed and fails when any of them raises a
// finding at medium severity or above.
//
// Each reviewer keeps a resumable session under
// .claude-scratch/agent-review/sessions/<push-key>/, keyed by branch and
// fork point so re-runs of the same push resume the reviewer with memory
// of what it asked for, while a new push (one whose fork point has moved)
// starts fresh on its own. Pass --restart to force a fresh review of the
// current push. The full finding set is written to
// .claude-scratch/agent-review/findings.json on every run.
//
// Usage:
//
//	go run ./internal/agentreview [--root DIR] [--base REF] [--restart]
//
// It shells out to the `claude` CLI, which must be on PATH.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const (
	maxDiffBytes  = 400_000
	maxConcurrent = 4
)

func main() {
	root := flag.String("root", ".", "repo root to review")
	base := flag.String("base", "origin/main", "base ref; the pushed diff is base...HEAD")
	restartFlag := flag.Bool("restart", false, "wipe reviewer sessions and review fresh")
	only := flag.String("only", "", "run only the named reviewer(s), comma-separated; empty runs the full roster")
	flag.Parse()

	roster := agents
	if *only != "" {
		roster = filterAgents(*only)
		if len(roster) == 0 {
			fail("--only %q matched no reviewer; known: %s", *only, strings.Join(agentNames(), ", "))
		}
	}

	abs, err := filepath.Abs(*root)
	if err != nil {
		fail("resolve root: %v", err)
	}

	bin, err := exec.LookPath("claude")
	if err != nil {
		fail("the `claude` CLI is required for the agent-review gate but was not found on PATH.\n" +
			"Install it, or skip this gate with `sparkwing run pre-push --bypass-agent-review` (then `git push --no-verify`).")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	files, diff, err := pushedDiff(ctx, abs, *base)
	if err != nil {
		fail("compute diff: %v", err)
	}
	if strings.TrimSpace(files) == "" {
		fmt.Println("agent-review: no changes vs", *base, "-- nothing to review")
		return
	}

	bucketDir := filepath.Join(abs, ".claude-scratch", "agent-review")
	key := sessionKey(ctx, abs, *base)
	sessionsDir := filepath.Join(bucketDir, "sessions", key)
	resuming := !*restartFlag && hasSessions(sessionsDir)
	if *restartFlag {
		_ = os.RemoveAll(sessionsDir)
		_ = os.Remove(filepath.Join(bucketDir, "findings.json"))
	}

	user := userPrompt(*base, files, diff)

	mode := "fresh review"
	if resuming {
		mode = "resuming this push"
	} else if *restartFlag {
		mode = "restarted -- fresh review"
	}
	fmt.Printf("agent-review: %d reviewers over %d changed file(s) [push %s, %s]\n",
		len(roster), strings.Count(strings.TrimSpace(files), "\n")+1, key, mode)

	var (
		mu     sync.Mutex
		all    []Finding
		failed []string
		sem    = make(chan struct{}, maxConcurrent)
		wg     sync.WaitGroup
	)
	for _, ag := range roster {
		wg.Add(1)
		go func(ag agentDef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			system, err := ag.systemPrompt()
			if err != nil {
				mu.Lock()
				failed = append(failed, fmt.Sprintf("%s: %v", ag.Name, err))
				mu.Unlock()
				return
			}
			sessionFile := filepath.Join(sessionsDir, ag.Name)
			fs, err := reviewWithRetry(ctx, bin, abs, ag, system, user, sessionFile, *restartFlag)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", ag.Name, err))
				fmt.Printf("  ✗ %s (review failed)\n", ag.Name)
				return
			}
			all = append(all, fs...)
			fmt.Printf("  ✓ %s (%d finding(s))\n", ag.Name, len(fs))
		}(ag)
	}
	wg.Wait()

	if err := writeBucket(filepath.Join(bucketDir, "findings.json"), all); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write findings bucket: %v\n", err)
	}

	fmt.Print(report(all))

	block := blocking(all)
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d reviewer(s) could not run:\n  - %s\n", len(failed), strings.Join(failed, "\n  - "))
	}
	if len(block) > 0 || len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "\nagent-review FAILED: %d finding(s) at medium or above.\n", len(block))
		fmt.Fprintln(os.Stderr, "Address them and re-run (reviewers resume with memory of this round),")
		fmt.Fprintln(os.Stderr, "or bypass with `sparkwing run pre-push --bypass-agent-review` then `git push --no-verify`.")
		os.Exit(1)
	}
	fmt.Println("\nagent-review: clean")
}

func reviewWithRetry(ctx context.Context, bin, root string, ag agentDef, system, user, sessionFile string, restart bool) ([]Finding, error) {
	fs, err := review(ctx, bin, root, ag, system, user, sessionFile, restart)
	if err == nil {
		return fs, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}
	return review(ctx, bin, root, ag, system, user, sessionFile, restart)
}

func userPrompt(base, files, diff string) string {
	truncated := ""
	if len(diff) > maxDiffBytes {
		diff = diff[:maxDiffBytes]
		truncated = "\n\n[diff truncated -- use Read/git to inspect the rest of the changed files listed above]"
	}
	return fmt.Sprintf(`Review the changes about to be pushed: the diff of %s...HEAD.

Changed files:
%s

Unified diff:
%s%s

Apply your mandate. Read surrounding code with Read/Grep when you need context -- the diff alone can mislead. Report every issue through the structured findings schema; return an empty findings array if the change is clean.

Severity discipline: medium, high, and blocker FAIL the push, so reserve them for real problems you are confident about. Use low for nits, style, and optional improvements. When unsure, prefer low or say nothing.

If you are resuming a prior review of this branch, recall what you flagged before: re-report only findings that the current diff has not addressed, and drop the ones that were fixed.`, base, files, diff, truncated)
}

// sessionKey identifies one logical push so reviewer sessions resume
// across iterations of the same work but start fresh for a new push. The
// key is the branch plus the fork point (the merge-base with base): adding
// fix commits leaves the fork point unchanged (same push, resume), while a
// landed push that advances base moves the fork point (new push, fresh).
func sessionKey(ctx context.Context, root, base string) string {
	branch := strings.TrimSpace(mustGit(ctx, root, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch == "" {
		branch = "HEAD"
	}
	fork, err := git(ctx, root, "merge-base", base, "HEAD")
	if err != nil {
		fork = mustGit(ctx, root, "rev-parse", "HEAD")
	}
	fork = strings.TrimSpace(fork)
	if len(fork) > 12 {
		fork = fork[:12]
	}
	if fork == "" {
		fork = "nofork"
	}
	return sanitizeKey(branch) + "-" + fork
}

func sanitizeKey(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}

func hasSessions(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func mustGit(ctx context.Context, root string, args ...string) string {
	out, _ := git(ctx, root, args...)
	return out
}

func pushedDiff(ctx context.Context, root, base string) (files, diff string, err error) {
	rangeSpec := base + "...HEAD"
	if _, e := git(ctx, root, "rev-parse", "--verify", "--quiet", base); e != nil {
		rangeSpec = "HEAD"
	}
	files, err = git(ctx, root, "diff", "--name-only", rangeSpec)
	if err != nil {
		return "", "", err
	}
	diff, err = git(ctx, root, "diff", rangeSpec)
	if err != nil {
		return "", "", err
	}
	return files, diff, nil
}

func git(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	return string(out), err
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "agent-review: "+format+"\n", a...)
	os.Exit(1)
}
