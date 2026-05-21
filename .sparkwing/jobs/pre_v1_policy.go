package jobs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// CheckPreV1Policy enforces the README's "this module stays below
// v1.0.0" policy across every place a stray v1+ marker could leak
// in. The companion check on the wire side is the version-gate in
// release.go which refuses to cut a v1.0.0+ tag locally; this check
// catches the indirect signals (CHANGELOG headers, VERSIONING.md
// statements, local git tags) so the policy can't drift via a doc
// edit or a misplaced manual tag.
//
// Returns nil when every signal is consistent with pre-v1, otherwise
// an aggregated error. Pre-existing v1.0.0+ tags from the proxy-cache
// poisoning incident are reported but do not fail the check (the
// cache cannot be undone; what we can do is keep ourselves honest
// about the policy going forward).
func CheckPreV1Policy(ctx context.Context, repoRoot string) error {
	var problems []string

	if msg := checkChangelogPreV1(filepath.Join(repoRoot, "CHANGELOG.md")); msg != "" {
		problems = append(problems, msg)
	}
	if msg := checkVersioningDocPreV1(filepath.Join(repoRoot, "VERSIONING.md")); msg != "" {
		problems = append(problems, msg)
	}
	if msg := checkLocalGitTagsPreV1(ctx, repoRoot); msg != "" {
		// Existing v1+ tags from the proxy-poisoning incident are
		// surfaced as a warning, not a failure. We can't delete them.
		// Newly-created v1+ tags would also show up here, but the
		// release pipeline's version-gate refuses to create them.
		fmt.Fprintf(os.Stderr, "pre-v1 policy: %s\n", msg)
	}

	if len(problems) > 0 {
		return fmt.Errorf("pre-v1 policy violations:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// changelogV1HeadingPattern matches Keep-a-Changelog version headers
// at major v1+ in either bracketed or bare form:
//
//	## [v1.0.0]
//	## [v1.0.0] - 2026-05-20
//	## v1.5.4
//	## [1.0.0]      (no v prefix, used by some Keep-a-Changelog setups)
var changelogV1HeadingPattern = regexp.MustCompile(`^##\s+\[?v?([1-9][0-9]*)\.\d+\.\d+`)

func checkChangelogPreV1(path string) string {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		return fmt.Sprintf("CHANGELOG.md: %v", err)
	}
	defer f.Close()
	var bad []string
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if changelogV1HeadingPattern.MatchString(line) {
			bad = append(bad, fmt.Sprintf("line %d: %q", lineNum, strings.TrimSpace(line)))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Sprintf("CHANGELOG.md: %v", err)
	}
	if len(bad) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"CHANGELOG.md contains v1.0.0+ release section(s); sparkwing is locked to v0.x:\n      %s",
		strings.Join(bad, "\n      "),
	)
}

// versioningDocV1Pattern catches explicit "v1.0.0 (released ...)"
// style assertions in VERSIONING.md. Discussion of the v1.0.0 cutover
// is allowed -- the doc absolutely should describe the policy -- but
// claiming a v1 release has already happened is the failure mode.
var versioningDocV1Pattern = regexp.MustCompile(`(?i)v1\.0\.0\s+(released|shipped|is the current|tagged)`)

func checkVersioningDocPreV1(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		return fmt.Sprintf("VERSIONING.md: %v", err)
	}
	if !versioningDocV1Pattern.Match(data) {
		return ""
	}
	// Find the offending line for the error message.
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if versioningDocV1Pattern.MatchString(scanner.Text()) {
			return fmt.Sprintf(
				"VERSIONING.md line %d asserts v1.0.0 has shipped; sparkwing is still pre-v1: %q",
				lineNum, strings.TrimSpace(scanner.Text()),
			)
		}
	}
	return "VERSIONING.md asserts v1.0.0 has shipped; sparkwing is still pre-v1"
}

// checkLocalGitTagsPreV1 returns a non-empty string listing any local
// v1+ tags. Surfaced as a warning by the caller (not a failure);
// existing tags from the proxy-cache poisoning incident can't be
// undone.
func checkLocalGitTagsPreV1(ctx context.Context, repoRoot string) string {
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "tag", "-l")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var bad []string
	for _, t := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		// Match v1+ at the start of the tag, anchored, allowing
		// optional patch suffixes. Skip submodule-style tags
		// (`pkg/foo/v1.0.0`) by requiring the v at index 0.
		if regexp.MustCompile(`^v[1-9]\d*\.\d+\.\d+`).MatchString(t) {
			bad = append(bad, t)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"local repo has %d v1.0.0+ tag(s) — these are permanent in the Go proxy cache and can't be undone, but the policy lock is still in force for future tags:\n      %s",
		len(bad), strings.Join(bad, " "),
	)
}
