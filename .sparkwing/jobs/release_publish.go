package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func publishRelease(ctx context.Context, repo, source, commit, version string) error {
	if source == "" {
		return errors.New("release: missing selected source commit; use sparkwing run release -- --commit SHA")
	}
	var refused error
	previousBase := ""
	for attempt := 0; attempt <= landMaxMerges; attempt++ {
		if err := fetchLandBase(ctx, gitIn(repo), repo); err != nil {
			return err
		}
		base, err := runGitIn(ctx, repo, "rev-parse", landBaseRef)
		if err != nil {
			return err
		}
		base = strings.TrimSpace(base)
		if base == previousBase {
			return refused
		}
		if _, err := runGitIn(ctx, repo, "merge-base", "--is-ancestor", source, base); err != nil {
			return fmt.Errorf("release: source %s is no longer on origin/main: %w", source, err)
		}
		merged, cleanup, err := mergeReleaseOnMain(ctx, repo, source, commit, base, version)
		if err != nil {
			return err
		}
		_, refused = runGitIn(ctx, merged, "push", "--atomic", "origin", "HEAD:refs/heads/main", "refs/tags/"+version)
		cleanup()
		if refused == nil {
			return nil
		}
		previousBase = base
	}
	return fmt.Errorf("release: main kept moving during publication; detached release commit %s is retained, retry publication: %w", commit, refused)
}

func mergeReleaseOnMain(ctx context.Context, repo, source, commit, base, version string) (dir string, cleanup func(), err error) {
	root, err := os.MkdirTemp("", "sparkwing-release-main-")
	if err != nil {
		return "", nil, err
	}
	dir = filepath.Join(root, "source")
	cleanup = func() {
		if _, removeErr := runGitIn(context.WithoutCancel(ctx), repo, "worktree", "remove", dir); removeErr != nil {
			sparkwing.Warn(ctx, "remove publication worktree %s: %v", dir, removeErr)
			return
		}
		if removeErr := os.RemoveAll(root); removeErr != nil {
			sparkwing.Warn(ctx, "remove publication directory %s: %v", root, removeErr)
		}
	}
	if _, err := runGitIn(ctx, repo, "worktree", "add", "--detach", dir, base); err != nil {
		return "", nil, errors.Join(err, os.RemoveAll(root))
	}
	if _, ancestorErr := runGitIn(ctx, repo, "merge-base", "--is-ancestor", commit, base); ancestorErr == nil {
		return dir, cleanup, nil
	}
	fail := func(err error) (string, func(), error) {
		return "", nil, fmt.Errorf("release: metadata merge retained at %s; resolve it before publication: %w", dir, err)
	}
	if _, err := runGitIn(ctx, dir, "merge", "--no-ff", "--no-commit", commit); err != nil {
		return fail(err)
	}
	var bodies []string
	for _, ref := range []string{source, commit, base} {
		body, err := runGitIn(ctx, repo, "show", ref+":CHANGELOG.md")
		if err != nil {
			return fail(err)
		}
		bodies = append(bodies, body)
	}
	body, err := releaseChangelogOnMain(bodies[0], bodies[1], bodies[2], version)
	if err != nil {
		return fail(err)
	}
	if err := writeChangelogPair(dir, body); err != nil {
		return fail(err)
	}
	if err := CheckChangelogLint(ctx, dir); err != nil {
		return fail(err)
	}
	if _, err := runGitIn(ctx, dir, "add", "--", "CHANGELOG.md", embeddedChangelogRel); err != nil {
		return fail(err)
	}
	// safety: a merge commit cannot name paths; its index comes only from the two committed trees and the notes above.
	if _, err := runGitIn(ctx, dir, "commit", "--no-verify", "-m", "chore(release): merge "+version+" metadata into main"); err != nil {
		return fail(err)
	}
	return dir, cleanup, nil
}

func releaseChangelogOnMain(source, released, main, version string) (string, error) {
	_, _, selected, err := changelogSectionBounds(source, "Unreleased")
	if err != nil {
		return "", err
	}
	start, end, current, err := changelogSectionBounds(main, "Unreleased")
	if err != nil {
		return "", err
	}
	cutStart, cutEnd, _, err := changelogSectionBounds(released, version)
	if err != nil {
		return "", err
	}
	// safety: subtract the selected entries, not the rolled text whose migration links have changed.
	_, pending, _ := strings.Cut(main[start:end], "\n")
	published := make(map[string]bool)
	for _, entry := range selected.Entries {
		published[strings.TrimSpace(entry.Body)] = true
	}
	for _, entry := range current.Entries {
		if published[strings.TrimSpace(entry.Body)] {
			pending = strings.Replace(pending, strings.TrimSpace(entry.Body), "", 1)
		}
	}
	parts := strings.Split(pending, "\n### ")
	for i := len(parts) - 1; i > 0; i-- {
		if !strings.Contains(parts[i], "\n- ") {
			parts = append(parts[:i], parts[i+1:]...)
		}
	}
	pending = strings.TrimSpace(strings.Join(parts, "\n### "))
	if pending != "" {
		pending += "\n\n"
	}
	return main[:start] + "## [Unreleased]\n\n" + pending + released[cutStart:cutEnd] + main[end:], nil
}

func changelogSectionBounds(body, version string) (int, int, docs.ReleaseSection, error) {
	for _, section := range docs.ParseChangelog(body) {
		if section.Version == version {
			return section.Start, section.End, section, nil
		}
	}
	return 0, 0, docs.ReleaseSection{}, fmt.Errorf("release: changelog has no [%s] section", version)
}
