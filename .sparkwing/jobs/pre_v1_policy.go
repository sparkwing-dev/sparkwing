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

func CheckPreV1Policy(ctx context.Context, repoRoot string) error {
	var problems []string

	if msg := checkChangelogPreV1(filepath.Join(repoRoot, "CHANGELOG.md")); msg != "" {
		problems = append(problems, msg)
	}
	if msg := checkVersioningDocPreV1(filepath.Join(repoRoot, "VERSIONING.md")); msg != "" {
		problems = append(problems, msg)
	}
	if msg := checkLocalGitTagsPreV1(ctx, repoRoot); msg != "" {
		fmt.Fprintf(os.Stderr, "pre-v1 policy: %s\n", msg)
	}

	if len(problems) > 0 {
		return fmt.Errorf("pre-v1 policy violations:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

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
		if regexp.MustCompile(`^v[1-9]\d*\.\d+\.\d+`).MatchString(t) {
			bad = append(bad, t)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"local repo has %d v1.0.0+ tag(s) -- these are permanent in the Go proxy cache and can't be undone, but the policy lock is still in force for future tags:\n      %s",
		len(bad), strings.Join(bad, " "),
	)
}
