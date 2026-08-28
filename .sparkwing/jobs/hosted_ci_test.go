package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readHostedCIFile(t *testing.T, rel string) string {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(body)
}

func workflowJob(t *testing.T, body, id string) string {
	t.Helper()
	marker := "\n  " + id + ":\n"
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("workflow has no %s job", id)
	}
	start += len(marker)
	tail := body[start:]
	for i, line := range strings.SplitAfter(tail, "\n") {
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(line, ":\n") {
			end := 0
			for _, prior := range strings.SplitAfter(tail, "\n")[:i] {
				end += len(prior)
			}
			return tail[:end]
		}
	}
	return tail
}

func requireWorkflowText(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("workflow is missing %q", want)
		}
	}
}

func TestHostedCITriggersCanonicalReadOnlyChecks(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/ci.yaml")
	requireWorkflowText(t, body,
		"  pull_request:\n",
		"  push:\n    branches: [main]\n",
		"permissions:\n  contents: read\n",
		"uses: ./.github/workflows/canonical-gates.yaml",
	)
	if strings.Contains(body, ": write") {
		t.Fatal("hosted CI grants a write permission")
	}
}

func TestCanonicalWorkflowRunsTheCheckedOutEventChange(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	requireWorkflowText(t, body,
		"  workflow_call:\n",
		"permissions:\n  contents: read\n",
		"gate: [pre-commit, pre-push]",
		"persist-credentials: false",
		"ref: ${{ github.sha }}",
		"git reset --soft \"$base\"",
		"git tag --delete -- \"$GITHUB_REF_NAME\"",
		"run pre-commit",
		"run pre-push",
		"bash bin/check-hosted-gate-clean.sh \"$TARGET_SHA\"",
	)
	if strings.Contains(body, ": write") {
		t.Fatal("canonical gates grant a write permission")
	}
}

func TestCanonicalWorkflowPinsEveryExternalAction(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	requireWorkflowText(t, body,
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1",
		"actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0",
		"actions/setup-node@820762786026740c76f36085b0efc47a31fe5020 # v7.0.0",
		"golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a # v9.3.0",
		"hashicorp/setup-terraform@dfe3c3f87815947d99a8997f908cb6525fc44e9e # v4.0.1",
		"actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2",
	)
}

func TestCanonicalWorkflowUploadsOnlyFailedBrowserEvidence(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	requireWorkflowText(t, body,
		"- name: Upload dashboard browser failure artifacts\n        if: ${{ failure() && matrix.gate == 'pre-commit' && hashFiles('web/test-results/.sparkwing-browser-failed') != '' }}\n        continue-on-error: true\n        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2\n        with:\n          name: dashboard-browser-${{ github.run_id }}-${{ github.run_attempt }}\n          path: |\n            web/test-results/\n            web/playwright-report/\n          if-no-files-found: ignore\n          retention-days: 14",
	)
}

func TestHostedMutationGuardCoversEveryGitStatus(t *testing.T) {
	body := readHostedCIFile(t, "bin/check-hosted-gate-clean.sh")
	requireWorkflowText(t, body,
		"actual_head=\"$(git rev-parse HEAD)\"",
		"git status --porcelain --untracked-files=all",
	)
}

func TestReleasePublicationDependsOnCanonicalChecks(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/release.yaml")
	jobsAt := strings.Index(body, "\njobs:\n")
	if jobsAt < 0 {
		t.Fatal("release workflow has no jobs section")
	}
	requireWorkflowText(t, body[:jobsAt],
		"workflow_dispatch:",
		"permissions:\n  actions: read\n  contents: read\n",
	)
	requireWorkflowText(t, workflowJob(t, body, "validate-tag"),
		"git ls-remote --exit-code --tags",
		`"refs/tags/$TAG^{}"`,
		`startswith("Verify tagged source / Canonical /")`,
		`select(.conclusion == "success")`,
		`test "$verified" = true`,
	)
	requireWorkflowText(t, workflowJob(t, body, "canonical"),
		"if: github.event_name == 'push'",
		"uses: ./.github/workflows/canonical-gates.yaml",
		"contents: read",
	)
	requireWorkflowText(t, workflowJob(t, body, "build"),
		"needs: [validate-tag, canonical]",
		"github.event_name == 'workflow_dispatch'",
		"ref: ${{ inputs.tag || github.sha }}",
		"path: .release-tools",
		".release-tools/bin/check-release-binary-vulnerabilities.sh",
		`go-version: "1.26.6"`,
	)
	requireWorkflowText(t, workflowJob(t, body, "build-images"),
		"needs: [validate-tag, canonical]",
		"github.event_name == 'workflow_dispatch'",
		"contents: read",
		"packages: write",
		"persist-credentials: false",
		"ref: ${{ inputs.tag || github.sha }}",
		`go-version: "1.26.6"`,
	)
	requireWorkflowText(t, workflowJob(t, body, "publish-images"),
		"contents: read",
		"id-token: write",
		"packages: write",
	)
	requireWorkflowText(t, workflowJob(t, body, "release"),
		"inputs.publish_images == false",
		"contents: write",
		"persist-credentials: false",
		"ref: ${{ inputs.tag || github.sha }}",
		`go-version: "1.26.6"`,
		"TAG: ${{ inputs.tag || github.ref_name }}",
	)
	requireWorkflowText(t, workflowJob(t, body, "prepare-binaries"),
		"always() && needs.build.result == 'success'",
	)

	for permission, want := range map[string]int{
		"contents: write": 1,
		"packages: write": 2,
		"id-token: write": 1,
	} {
		if got := strings.Count(body, permission); got != want {
			t.Errorf("%s occurs %d times, want %d", permission, got, want)
		}
	}
}

func TestREADMEPublishesStableCIBadge(t *testing.T) {
	body := readHostedCIFile(t, "README.md")
	requireWorkflowText(t, body,
		"actions/workflows/ci.yaml/badge.svg?branch=main",
		"actions/workflows/ci.yaml)",
	)
}
