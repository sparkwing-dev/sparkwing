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
	return "Judge a release tag's source: changelog section, schema and wire breaks"
}

func (ReleaseVerify) Help() string {
	return "Reads the version a tag names and checks the source it points at carries what that release owes: " +
		"a CHANGELOG.md [vX.Y.Z] section with at least one entry (the hosted release publishes that section as the " +
		"release notes, so an empty one would half-release), a changelog account of any runs-store schema change " +
		"since the previous release tag, and one of any wire-format cut. It reads files and git, changes nothing, " +
		"and never reaches for a branch tip. The hosted release workflow runs it in the validate stage, so a tag " +
		"pushed by hand is judged the same way one cut by `sparkwing run release` is."
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
	return nil, nil
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
