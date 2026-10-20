// Command guardcheck holds the architecture guards to their allow-lists in
// .guards/. It runs the depguard and forbidigo rules in .golangci-guards.yml
// over the whole tree and counts the internal/ unexport candidates, then fails
// when a finding is missing from its allow-list, when an entry no longer
// matches a finding, or when an allow-list grew against the base ref.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
)

const (
	guardDir      = ".guards"
	guardConfig   = ".golangci-guards.yml"
	unexportAllow = "unexport.allow"
)

func main() {
	fast := flag.Bool("fast", false, "judge only the depguard rules, which need no type information (the pre-push tier)")
	unexport := flag.Bool("unexport", true, "count internal/ unexport candidates (loads every package with types; about a minute)")
	base := flag.String("base", envOr("SPARKWING_GUARD_BASE", "origin/main"), "git ref whose allow-lists bound growth; empty skips the growth check")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: guardcheck [flags] <repo>\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	gitenv.Unbind()
	problems, err := run(flag.Arg(0), *fast, *unexport && !*fast, *base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "guardcheck:", err)
		os.Exit(2)
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, p)
		}
		fmt.Fprintf(os.Stderr, "guardcheck: %d problem(s); .guards/README.md explains each allow-list\n", len(problems))
		os.Exit(1)
	}
	fmt.Println("guardcheck: every guard matches its allow-list")
}

func envOr(name, fallback string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return fallback
}

func run(repo string, fast, unexport bool, base string) ([]string, error) {
	repo, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	findings, err := lintFindings(repo, fast)
	if err != nil {
		return nil, err
	}
	allows, err := readAllowDir(filepath.Join(repo, guardDir))
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, name := range unionKeys(findings, allows) {
		lint := strings.HasPrefix(name, "depguard-") || (!fast && strings.HasPrefix(name, "forbidigo-"))
		if !lint {
			continue
		}
		problems = append(problems, compareEntries(name, findings[name], allows[name])...)
	}
	if unexport {
		counts, names, err := unexportCandidates(repo)
		if err != nil {
			return nil, err
		}
		problems = append(problems, compareCounts(counts, names, allows[unexportAllow])...)
	}
	if base != "" {
		grown, err := growth(repo, base, allows)
		if err != nil {
			fmt.Fprintf(os.Stderr, "guardcheck: growth check skipped: %v\n", err)
		}
		problems = append(problems, grown...)
	}
	return problems, nil
}

type lintIssue struct {
	FromLinter string
	Text       string
	Pos        struct{ Filename string }
}

var (
	depguardText  = regexp.MustCompile(`^import '([^']+)' is not allowed from list '([^']+)'`)
	forbidigoText = regexp.MustCompile("^use of `([^`]+)` forbidden because \"(F\\d+):")
)

func lintFindings(repo string, fast bool) (map[string][]string, error) {
	args := []string{"run", "-c", guardConfig, "--output.json.path=stdout", "--output.text.path=" + os.DevNull, "--show-stats=false", "--allow-parallel-runners"}
	if fast {
		args = append(args, "--enable-only=depguard")
	}
	cmd := exec.Command("golangci-lint", append(args, "./...")...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GOLANGCI_LINT_CACHE="+lintCacheDir(repo))
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
	var exit *exec.ExitError
	if runErr != nil && !(errors.As(runErr, &exit) && exit.ExitCode() == 1) {
		return nil, fmt.Errorf("golangci-lint -c %s: %w", guardConfig, runErr)
	}
	var report struct{ Issues []lintIssue }
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		return nil, fmt.Errorf("golangci-lint JSON: %w", err)
	}
	return classify(report.Issues)
}

// safety: golangci-lint replays a cached finding under the path of the tree
// that filled the cache, so a cache shared between worktrees reports another
// checkout's files. Each worktree gets its own cache, whatever the caller set.
func lintCacheDir(repo string) string {
	return filepath.Join(repo, ".sparkwing", "cache", "golangci-guards")
}

func classify(issues []lintIssue) (map[string][]string, error) {
	out := map[string][]string{}
	for _, issue := range issues {
		file := filepath.ToSlash(issue.Pos.Filename)
		if filepath.IsAbs(issue.Pos.Filename) || file == ".." || strings.HasPrefix(file, "../") {
			return nil, fmt.Errorf("finding outside the checked tree, %s: a lint cache shared with another worktree replays its paths", file)
		}
		switch issue.FromLinter {
		case "depguard":
			m := depguardText.FindStringSubmatch(issue.Text)
			if m == nil {
				return nil, fmt.Errorf("unrecognized depguard finding %s: %s", file, issue.Text)
			}
			name := "depguard-" + m[2] + ".allow"
			out[name] = append(out[name], path.Dir(file)+"\t"+m[1])
		case "forbidigo":
			m := forbidigoText.FindStringSubmatch(issue.Text)
			if m == nil {
				return nil, fmt.Errorf("unrecognized forbidigo finding %s: %s", file, issue.Text)
			}
			name := "forbidigo-" + strings.ToLower(m[2]) + ".allow"
			call := m[1][strings.LastIndex(m[1], ".")+1:]
			out[name] = append(out[name], file+"\t"+call)
		default:
			return nil, fmt.Errorf("unexpected %s finding %s: %s", issue.FromLinter, file, issue.Text)
		}
	}
	for name, entries := range out {
		if strings.HasPrefix(name, "depguard-") {
			out[name] = dedupe(entries)
		}
	}
	return out, nil
}

func dedupe(entries []string) []string {
	sort.Strings(entries)
	out := entries[:0]
	for i, e := range entries {
		if i == 0 || e != entries[i-1] {
			out = append(out, e)
		}
	}
	return out
}

func readAllowDir(dir string) (map[string][]string, error) {
	out := map[string][]string{}
	matches, err := filepath.Glob(filepath.Join(dir, "*.allow"))
	if err != nil {
		return nil, err
	}
	for _, file := range matches {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		out[filepath.Base(file)] = parseAllow(string(raw))
	}
	return out, nil
}

func parseAllow(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func unionKeys(a, b map[string][]string) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// safety: entries compare as a multiset, so a second forbidden call in a file
// that already holds one fails rather than hiding behind the first entry.
func compareEntries(name string, found, allowed []string) []string {
	extra, stale := multisetDiff(found, allowed)
	var out []string
	for _, e := range extra {
		out = append(out, fmt.Sprintf("NEW  %s: %s (fix the code; a guard allow-list only shrinks)", name, e))
	}
	for _, e := range stale {
		out = append(out, fmt.Sprintf("GONE %s: %s (delete this line from .guards/%s)", name, e, name))
	}
	return out
}

func multisetDiff(a, b []string) (onlyA, onlyB []string) {
	count := map[string]int{}
	for _, e := range a {
		count[e]++
	}
	for _, e := range b {
		count[e]--
	}
	for _, e := range unionKeys(map[string][]string{}, toSet(count)) {
		for n := count[e]; n > 0; n-- {
			onlyA = append(onlyA, e)
		}
		for n := count[e]; n < 0; n++ {
			onlyB = append(onlyB, e)
		}
	}
	return onlyA, onlyB
}

func toSet(count map[string]int) map[string][]string {
	out := map[string][]string{}
	for k := range count {
		out[k] = nil
	}
	return out
}

func parseCounts(entries []string) (map[string]int, error) {
	out := map[string]int{}
	for _, e := range entries {
		pkg, n, ok := strings.Cut(e, "\t")
		count, err := strconv.Atoi(strings.TrimSpace(n))
		if !ok || err != nil {
			return nil, fmt.Errorf("%s: want <package><TAB><count>, got %q", unexportAllow, e)
		}
		out[pkg] = count
	}
	return out, nil
}

func compareCounts(counts map[string]int, names map[string][]string, allowed []string) []string {
	limits, err := parseCounts(allowed)
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	keys := map[string]bool{}
	for k := range counts {
		keys[k] = true
	}
	for k := range limits {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, pkg := range sorted {
		got, want := counts[pkg], limits[pkg]
		switch {
		case got > want:
			out = append(out, fmt.Sprintf("NEW  %s: %s has %d exported identifiers no other package uses, allow-list says %d; unexport them: %s",
				unexportAllow, pkg, got, want, strings.Join(names[pkg], ", ")))
		case got < want && got == 0:
			out = append(out, fmt.Sprintf("GONE %s: %s has no unexport candidates left (delete its line from .guards/%s)", unexportAllow, pkg, unexportAllow))
		case got < want:
			out = append(out, fmt.Sprintf("GONE %s: %s has %d unexport candidates (lower its count from %d in .guards/%s)", unexportAllow, pkg, got, want, unexportAllow))
		}
	}
	return out
}

func growth(repo, base string, allows map[string][]string) ([]string, error) {
	mb, err := git(repo, "merge-base", "HEAD", base)
	if err != nil {
		return nil, fmt.Errorf("no merge base with %s: %w", base, err)
	}
	mb = strings.TrimSpace(mb)
	var out []string
	for _, name := range unionKeys(allows, nil) {
		raw, err := git(repo, "show", mb+":"+guardDir+"/"+name)
		if err != nil {
			continue
		}
		before := parseAllow(raw)
		if name == unexportAllow {
			was, errWas := parseCounts(before)
			now, errNow := parseCounts(allows[name])
			if errWas != nil || errNow != nil {
				continue
			}
			for _, pkg := range unionKeys(toSet(now), nil) {
				if now[pkg] > was[pkg] {
					out = append(out, fmt.Sprintf("GREW %s: %s rose from %d to %d against %s; allow-lists only shrink", name, pkg, was[pkg], now[pkg], base))
				}
			}
			continue
		}
		added, _ := multisetDiff(allows[name], before)
		for _, e := range added {
			out = append(out, fmt.Sprintf("GREW %s: %s is not in %s's list; allow-lists only shrink", name, e, base))
		}
	}
	return out, nil
}

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}
