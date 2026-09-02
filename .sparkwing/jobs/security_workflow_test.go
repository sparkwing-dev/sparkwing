package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSecurityWorkflowTriggersAndPermissions(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/security.yaml")
	jobsAt := strings.Index(body, "\njobs:\n")
	if jobsAt < 0 {
		t.Fatal("security workflow has no jobs section")
	}
	requireWorkflowText(t, body[:jobsAt],
		"  workflow_call:\n    inputs:\n      source_ref:\n",
		"  pull_request:\n",
		"  push:\n    branches: [main]\n",
		"  schedule:\n",
		"permissions:\n  contents: read\n",
	)
	if strings.Contains(body, "pull_request_target") {
		t.Fatal("security workflow executes untrusted code through pull_request_target")
	}
	if got := strings.Count(body, "security-events: write"); got != 2 {
		t.Fatalf("security-events write grants = %d, want one per code-scanning job", got)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, ": write") && strings.TrimSpace(line) != "security-events: write" {
			t.Errorf("unexpected write permission: %s", strings.TrimSpace(line))
		}
	}
}

func TestSecurityWorkflowKeepsForksReadOnly(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/security.yaml")
	requireWorkflowText(t, body,
		"persist-credentials: false",
		"if: github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository",
	)
	if got := strings.Count(body, "persist-credentials: false"); got != 2 {
		t.Fatalf("non-persisting checkouts = %d, want 2", got)
	}
	scanAt := strings.Index(body, `run: '"$RUNNER_TEMP/sparkwing" run security-scan`)
	uploadAt := strings.Index(body, "- name: Upload gosec findings to code scanning")
	if scanAt < 0 || uploadAt < scanAt {
		t.Fatal("fork-safe SARIF condition can bypass scanner execution")
	}
}

func TestSecurityWorkflowPinsExternalActions(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/security.yaml")
	pinned := regexp.MustCompile(`^[0-9a-f]{40}$`)
	uses := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if !strings.HasPrefix(line, "uses: ") {
			continue
		}
		uses++
		action := strings.Fields(strings.TrimPrefix(line, "uses: "))[0]
		_, ref, ok := strings.Cut(action, "@")
		if !ok || !pinned.MatchString(ref) {
			t.Errorf("external action is not pinned to a full commit SHA: %s", action)
		}
	}
	if uses != 8 {
		t.Fatalf("external action uses = %d, want 8", uses)
	}
}

func TestReleaseCheckoutsNeverPersistCredentials(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/release.yaml")
	checkouts := strings.Count(body, "uses: actions/checkout@")
	if checkouts == 0 {
		t.Fatal("release workflow has no checkout steps")
	}
	if got := strings.Count(body, "persist-credentials: false"); got != checkouts {
		t.Fatalf("non-persisting checkouts = %d, want %d", got, checkouts)
	}
}

func TestEveryWorkflowPinsActionsByCommitSHA(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	version := regexp.MustCompile(`^# v\d+\.\d+\.\d+$`)
	workflows := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		workflows++
		for i, line := range strings.Split(readHostedCIFile(t, ".github/workflows/"+name), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
			if !strings.HasPrefix(line, "uses: ") {
				continue
			}
			fields := strings.Fields(strings.TrimPrefix(line, "uses: "))
			action := fields[0]
			if strings.HasPrefix(action, "./") {
				continue
			}
			where := fmt.Sprintf("%s:%d", name, i+1)
			_, ref, ok := strings.Cut(action, "@")
			if !ok || !sha.MatchString(ref) {
				t.Errorf("%s: action is not pinned to a full commit SHA: %s", where, action)
				continue
			}
			if comment := strings.Join(fields[1:], " "); !version.MatchString(comment) {
				t.Errorf("%s: pinned action lacks a %q version comment: %s", where, "# vX.Y.Z", line)
			}
		}
	}
	if workflows < 4 {
		t.Fatalf("workflows scanned = %d, want every file under .github/workflows", workflows)
	}
}

func TestSecurityWorkflowHasNoPrivateInfrastructureSurface(t *testing.T) {
	body := strings.ToLower(readHostedCIFile(t, ".github/workflows/security.yaml"))
	for _, forbidden := range []string{
		"aws-actions", "configure-aws", "amazonaws", "run: aws", "ecr", "kube", "helm", "s3://",
		"ghcr.io", "docker/login-action", "id-token", "packages: write", "secrets.", "private",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("security workflow contains private-infrastructure surface %q", forbidden)
		}
	}
}

func TestReleaseWaitsForExactSourceSecurityWorkflow(t *testing.T) {
	release := readHostedCIFile(t, ".github/workflows/release.yaml")
	requireWorkflowText(t, workflowJob(t, release, "security"),
		"needs: validate-tag",
		"uses: ./.github/workflows/security.yaml",
		"source_ref: ${{ needs.validate-tag.outputs.source_sha }}",
		"contents: read",
		"security-events: write",
	)
	for _, id := range []string{"build", "build-images"} {
		job := workflowJob(t, release, id)
		requireWorkflowText(t, job,
			"needs: [validate-tag, canonical, security]",
			"needs.security.result == 'success'",
		)
	}
	security := readHostedCIFile(t, ".github/workflows/security.yaml")
	if got := strings.Count(security, "ref: ${{ inputs.source_ref || github.sha }}"); got != 2 {
		t.Fatalf("exact-source security checkouts = %d, want scanner and CodeQL", got)
	}
}

func TestGitleaksExclusionsCannotHideRepositoryPaths(t *testing.T) {
	body := readHostedCIFile(t, ".gitleaks.toml")
	if strings.Contains(body, "paths =") {
		t.Fatal("gitleaks allow-list excludes a repository path")
	}
	if got := strings.Count(body, "[[allowlists]]"); got != 2 {
		t.Fatalf("gitleaks allow-list entries = %d, want 2 exact fixture values", got)
	}
	requireWorkflowText(t, body,
		"sw_a1b2c3d4e5f6g7h8i9j0k1l2m3n4",
		"abcd1234-ef567890",
	)
	ignore := strings.TrimSpace(readHostedCIFile(t, ".gitleaksignore"))
	want := strings.Join([]string{
		"49be43e4b2e9c0dc24ee7d50eaf4a0dd0291e147:internal/web/next-out/_next/static/chunks/17mq5rf2.ibbt.js:generic-api-key:55",
		"c4f254125ffc14690be63b70e25c21c90e95932c:pkg/storage/segment_artifact_test.go:generic-api-key:6",
	}, "\n")
	if ignore != want {
		t.Fatalf("gitleaks history exceptions are not the exact fingerprint set: %q", ignore)
	}
}
