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
		"actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4.4.0",
		"actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5.6.0",
		"actions/setup-node@49933ea5288caeca8642d1e84afbd3f7d6820020 # v4.4.0",
		"golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a # v9.3.0",
		"hashicorp/setup-terraform@dfe3c3f87815947d99a8997f908cb6525fc44e9e # v4.0.1",
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
	requireWorkflowText(t, body[:jobsAt], "permissions:\n  contents: read\n")
	requireWorkflowText(t, workflowJob(t, body, "canonical"),
		"uses: ./.github/workflows/canonical-gates.yaml",
		"contents: read",
	)
	requireWorkflowText(t, workflowJob(t, body, "build"), "needs: canonical")
	requireWorkflowText(t, workflowJob(t, body, "build-images"),
		"needs: canonical",
		"contents: read",
		"packages: write",
		"persist-credentials: false",
		"ref: ${{ github.sha }}",
	)
	requireWorkflowText(t, workflowJob(t, body, "publish-images"),
		"contents: read",
		"id-token: write",
		"packages: write",
	)
	requireWorkflowText(t, workflowJob(t, body, "release"),
		"contents: write",
		"persist-credentials: false",
		"ref: ${{ github.sha }}",
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
