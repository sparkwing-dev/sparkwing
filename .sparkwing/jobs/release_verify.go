package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type ReleaseVerifyArgs struct {
	Version string `flag:"version" desc:"Release tag to verify, e.g. v0.50.4"`
}

// ReleaseVerify judges a tag's source against the version the tag names,
// before anything is built or published.
type ReleaseVerify struct {
	sparkwing.Base
	args ReleaseVerifyArgs
}

func (ReleaseVerify) ShortHelp() string {
	return "Judge a release tag's source: changelog section, migration guide, schema and wire breaks"
}

func (ReleaseVerify) Help() string {
	return "Reads the version a tag names and checks the source it points at carries what that release owes: " +
		"a CHANGELOG.md [vX.Y.Z] section with at least one entry (the hosted release publishes that section as the " +
		"release notes, and falls back to the tag message when it is missing), a changelog account of any runs-store schema change " +
		"since the previous release tag, one of any wire-format cut, and a rolled migration guide whose sections every " +
		"(Breaking) entry links. It reads files and git, changes nothing, " +
		"and never reaches for a branch tip. The hosted release workflow no longer runs it, so this is the check to " +
		"run by hand before a tag goes out. The CI/CD group is reintroducing it there deliberately."
}

func (ReleaseVerify) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Judge the tag before it builds", Command: "sparkwing run release-verify --version v0.50.4"},
	}
}

func (v *ReleaseVerify) Plan(_ context.Context, plan *sparkwing.Plan, in ReleaseVerifyArgs, rc sparkwing.RunContext) error {
	v.args = in
	sparkwing.Job(plan, rc.Pipeline, v)
	return nil
}

func (v *ReleaseVerify) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	version := strings.TrimSpace(v.args.Version)
	repoDir, err := repoRoot()
	if err != nil {
		return nil, fmt.Errorf("release-verify: locate repo root: %w", err)
	}
	sparkwing.Step(w, "changelog-section", func(ctx context.Context) error {
		return checkChangelogSection(ctx, repoDir, version)
	}).SafeWithoutDryRun()
	sparkwing.Step(w, "schema-changelog", func(ctx context.Context) error {
		if err := requireVerifiableVersion(version); err != nil {
			return err
		}
		return checkSchemaBreak(ctx, repoDir, version)
	}).SafeWithoutDryRun()
	sparkwing.Step(w, "wire-changelog", func(ctx context.Context) error {
		if err := requireVerifiableVersion(version); err != nil {
			return err
		}
		return checkWireBreak(ctx, repoDir, version)
	}).SafeWithoutDryRun()
	sparkwing.Step(w, "migration-guide", func(ctx context.Context) error {
		return checkMigrationGuide(ctx, repoDir, version)
	}).SafeWithoutDryRun()
	return nil, nil
}

// safety: the tag is what adopters read, so a (Breaking) entry that still
// points at _unreleased.md, or at a guide the tag does not carry, is caught
// before anything is built rather than by the next commit on main.
func checkMigrationGuide(ctx context.Context, repoDir, version string) error {
	if err := requireVerifiableVersion(version); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(repoDir, "CHANGELOG.md"))
	if err != nil {
		return fmt.Errorf("release-verify: read CHANGELOG.md: %w", err)
	}
	sec, ok := releasedSection(string(body), version)
	if !ok {
		return fmt.Errorf("release-verify: CHANGELOG.md carries no [%s] section", version)
	}
	breaking := breakingEntries(sec)
	if len(breaking) == 0 {
		sparkwing.Info(ctx, "[%s] carries no (Breaking) entry, so the tag owes no migration guide", version)
		return nil
	}
	if issues := lintSectionBreakingEntries(sec, migrationsFS(repoDir)); len(issues) > 0 {
		var b strings.Builder
		for _, i := range issues {
			b.WriteString(i.Format())
			b.WriteByte('\n')
		}
		return fmt.Errorf("release-verify: the migration guide %s owes its %d (Breaking) entries is not rolled:\n%s"+
			"cut a patch tag whose %s/%s.md carries a section per entry", version, len(breaking), b.String(), migrationsDirRel, version)
	}
	index, err := readMigrationFile(repoDir, migrationIndexName)
	if err != nil {
		return fmt.Errorf("release-verify: %w", err)
	}
	if !indexHasVersionRow(index, version) {
		return fmt.Errorf("release-verify: %s/%s has no row for %s, so an adopter walking the guides in order skips it; add the row",
			migrationsDirRel, migrationIndexName, version)
	}
	sparkwing.Info(ctx, "%d (Breaking) entries in [%s] link a section of %s/%s.md, and the index carries its row",
		len(breaking), version, migrationsDirRel, version)
	return nil
}

func requireVerifiableVersion(version string) error {
	if version == "" {
		return fmt.Errorf("release-verify: --version is required (e.g. --version v0.50.4)")
	}
	if !isReleaseTagShape(version) {
		return fmt.Errorf("release-verify: version %q is not a vMAJOR.MINOR.PATCH release tag "+
			"(an optional -prerelease suffix is allowed; leading zeros and +build metadata are not)", version)
	}
	return nil
}

func checkChangelogSection(ctx context.Context, repoDir, version string) error {
	if err := requireVerifiableVersion(version); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(repoDir, "CHANGELOG.md"))
	if err != nil {
		return fmt.Errorf("release-verify: read CHANGELOG.md: %w", err)
	}
	entries, err := versionEntries(string(body), version)
	if err != nil {
		return fmt.Errorf("release-verify: %w", err)
	}
	if entries == 0 {
		return fmt.Errorf("release-verify: CHANGELOG.md carries no [%s] section with entries; "+
			"the release notes are that section, so publishing would ship an empty release", version)
	}
	sparkwing.Info(ctx, "CHANGELOG.md [%s] carries %d entries", version, entries)
	return nil
}

func init() {
	sparkwing.Register[ReleaseVerifyArgs]("release-verify", func() sparkwing.Pipeline[ReleaseVerifyArgs] {
		return &ReleaseVerify{}
	})
}
