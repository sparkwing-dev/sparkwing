package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const policyJobID = "policy"

// perf: every step here answers in seconds with a warm build cache. The
// allowance covers a fresh hosted runner compiling the checkers and the CLI
// for the first time.
const policyJobTimeout = 10 * time.Minute

// perf: the policy stage runs before every heavy job of gate and land. Most
// failed lands were a one-second policy finding reported only after a
// twenty-minute test, race and review run; ahead of them, it fails in seconds.
type policyJob struct {
	sparkwing.Base
	Proven sparkwing.Ref[gatePass]
	Head   sparkwing.Ref[landHead]
	Checks *queuedChecks
}

// safety: CollectAll, so one run names every policy finding rather than the
// first; each step is cheap enough that finishing the rest costs seconds.
func (j *policyJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.FailurePolicy(sparkwing.CollectAll)
	for _, s := range policySteps {
		sparkwing.Step(w, s.id, j.Checks.wrap("policy/"+s.id, j.Head, s.run))
	}
	return nil, nil
}

type policyStep struct {
	id  string
	run func(context.Context) error
}

// safety: every heavy job waits for these cheap checks.
var policySteps = []policyStep{
	{"gofmt", runGofmt},
	{"windows-compile", runWindowsCompile},
	{"formatters", runFormatters},
	{"em-dashes", checkEmDashes},
	{"tracker-ids", checkTrackerIDs},
	{"tracked-binaries", checkTrackedBinaries},
	{"docs-mirror", checkDocsMirror},
	{"docs-sidebar", checkDocsSidebar},
	{"changelog-links", checkTrackedChangelogLinks},
	{"comments", trackedPolicyChecker("commentcheck", "Go file", existingGoFiles)},
	{"test-sleeps", trackedPolicyChecker("sleepcheck", "test file", existingGoTestFiles)},
	{"home-resolution", checkHomeResolution},
	{"changelog", preReleaseShell("bash bin/check-changelog.sh --tracked")},
	{"api-spec", checkAPISpec},
	{"api-snapshot", checkTrackedAPISnapshot},
	{"cli-docs", checkCLIDocs},
	{"markdownlint", runMarkdownlint},
	{"config-reference", preReleaseShell("bash bin/gen-config-docs.sh --check")},
	{"sdk-version", checkTypeScriptSDKVersion},
	{"wire-cut-coverage", preReleaseShell("git describe --tags --abbrev=0 --match 'v[0-9]*' HEAD >/dev/null || { echo 'No reachable release tag; run git fetch --tags origin and retry' >&2; exit 1; }; go -C .sparkwing test -p 2 ./jobs -run ^TestRepositoryUnreleasedSectionDeclaresEveryWireCut$ -count=1")},
}

func checkCLIDocs(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "bash bin/gen-cli-docs.sh --check").Env("GOWORK", "auto").Run(); err != nil {
		return fmt.Errorf("the generated CLI reference in docs/ differs from the command registry; run `bash bin/gen-cli-docs.sh` and commit docs/ and pkg/docs: %w", err)
	}
	return nil
}

func checkDocsSidebar(ctx context.Context) error {
	return withTrackedPolicyDocs(ctx, func(root string) error {
		if result, err := sparkwing.Bash(ctx, "go run ./internal/doccheck --sidebar "+shellQuote(filepath.Join(root, "docs"))).Env("GOWORK", "auto").Run(); err != nil {
			return fmt.Errorf("docs sidebar differs from the docs tree; add each page to docs/_sidebar.json or its excluded list:\n%s%w", result.Stdout, err)
		}
		return nil
	})
}

func checkTrackedChangelogLinks(ctx context.Context) error {
	err := withTrackedPolicyDocs(ctx, func(root string) error { return CheckChangelogLint(ctx, root) })
	if err != nil {
		return fmt.Errorf("a released changelog entry may be edited to repair a link: restore the document, or repoint at a github.com/sparkwing-dev/sparkwing/blob/<commit-or-tag>/ permalink that still carries it: %w", err)
	}
	return nil
}

func checkTrackedAPISnapshot(ctx context.Context) error {
	err := withTrackedPolicyFiles(ctx, []string{".apidiff", "pkg", "sparkwing", "sparkwingtest", "go.mod", "go.sum"}, func(root string) error {
		if _, err := sparkwing.Exec(ctx, "go", "run", filepath.Join(regexCheckRoot(), "cmd/apidiff/main.go"), "generated").Dir(root).Env("GOWORK", "off").Capture(); err != nil {
			var execErr *sparkwing.ExecError
			if errors.As(err, &execErr) {
				diagnostic := *execErr
				diagnostic.Stderr = strings.ReplaceAll(execErr.Stderr, root+string(os.PathSeparator), "")
				return fmt.Errorf("API snapshot generation failed: %w", &diagnostic)
			}
			return fmt.Errorf("API snapshot generation failed: %w", err)
		}
		_, err := sparkwing.Exec(ctx, "diff", "-r", "-u", ".apidiff", "generated").Dir(root).Run()
		return err
	})
	if err != nil {
		return fmt.Errorf("the public API surface drifted from .apidiff/; record the change in CHANGELOG.md, then run `bash bin/regen-api-snapshot.sh` and stage .apidiff/: %w", err)
	}
	return nil
}

func withTrackedPolicyDocs(ctx context.Context, check func(string) error) error {
	return withTrackedPolicyFiles(ctx, []string{"docs", "CHANGELOG.md"}, check)
}

func withTrackedPolicyFiles(ctx context.Context, paths []string, check func(string) error) (resultErr error) {
	files, err := listNames(ctx, "ls-files -z -- "+shellQuoteAll(paths))
	if err != nil {
		return err
	}
	snapshot, err := os.MkdirTemp("", "sparkwing-policy-files-*")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(snapshot)) }()
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(regexCheckRoot(), file))
		if err != nil {
			return err
		}
		target := filepath.Join(snapshot, file)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		// #nosec G703 -- target is a git ls-files path under a fresh MkdirTemp dir; git rejects absolute and .. paths.
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return err
		}
	}
	return check(snapshot)
}

func trackedPolicyChecker(tool, noun string, keep func([]string) []string) func(context.Context) error {
	return func(ctx context.Context) error {
		command, scope, err := scopedCheckerCommand(ctx, tool, noun, keep, true)
		if err != nil {
			return err
		}
		sparkwing.Info(ctx, "tracked source only: %s", scope)
		_, err = sparkwing.Bash(ctx, command).Env("GOWORK", "auto").Run()
		return err
	}
}
