package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

type workflowStep struct {
	file    string
	job     string
	line    int
	uses    string
	comment string
	with    map[string]string
}

func (s workflowStep) where() string {
	return fmt.Sprintf("%s:%d (job %s)", s.file, s.line, s.job)
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func trailingComment(lines []string, line int, value string) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	raw := lines[line-1]
	at := strings.Index(raw, value)
	if at < 0 {
		return ""
	}
	return strings.TrimSpace(raw[at+len(value):])
}

func workflowUsesSteps(t *testing.T, name string) []workflowStep {
	t.Helper()
	body := readHostedCIFile(t, ".github/workflows/"+name)
	lines := strings.Split(body, "\n")
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	jobs := mappingValue(&doc, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		t.Fatalf("%s has no jobs mapping", name)
	}
	var steps []workflowStep
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		id := jobs.Content[i].Value
		seq := mappingValue(jobs.Content[i+1], "steps")
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		for _, node := range seq.Content {
			uses := mappingValue(node, "uses")
			if uses == nil {
				continue
			}
			step := workflowStep{
				file:    name,
				job:     id,
				line:    uses.Line,
				uses:    uses.Value,
				comment: trailingComment(lines, uses.Line, uses.Value),
				with:    map[string]string{},
			}
			if with := mappingValue(node, "with"); with != nil && with.Kind == yaml.MappingNode {
				for j := 0; j+1 < len(with.Content); j += 2 {
					step.with[with.Content[j].Value] = with.Content[j+1].Value
				}
			}
			steps = append(steps, step)
		}
	}
	return steps
}

func workflowFileNames(t *testing.T) []string {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		names = append(names, name)
	}
	return names
}

func actionPinTable(t *testing.T) map[string]string {
	t.Helper()
	action := regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)
	version := regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	table := map[string]string{}
	for i, line := range strings.Split(readHostedCIFile(t, ".github/action-pins.txt"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !action.MatchString(fields[0]) || !version.MatchString(fields[1]) {
			t.Fatalf("action-pins.txt:%d is not %q: %s", i+1, "<owner>/<repo>[/<path>]@<sha> vX.Y.Z", line)
		}
		if _, duplicate := table[fields[0]]; duplicate {
			t.Fatalf("action-pins.txt:%d repeats %s", i+1, fields[0])
		}
		table[fields[0]] = fields[1]
	}
	if len(table) == 0 {
		t.Fatal("action-pins.txt records no pins")
	}
	return table
}

func requireCheckoutsDropCredentials(t *testing.T, name string) int {
	t.Helper()
	checkouts := 0
	for _, step := range workflowUsesSteps(t, name) {
		if !strings.HasPrefix(step.uses, "actions/checkout@") {
			continue
		}
		checkouts++
		if got, ok := step.with["persist-credentials"]; !ok || got != "false" {
			t.Errorf("%s: checkout leaves the job token on disk (persist-credentials = %q)", step.where(), got)
		}
	}
	return checkouts
}

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
		"if: github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository",
	)
	if got := requireCheckoutsDropCredentials(t, "security.yaml"); got != 2 {
		t.Fatalf("non-persisting checkouts = %d, want 2", got)
	}
	scanAt := strings.Index(body, `run: '"$RUNNER_TEMP/sparkwing" run security-scan`)
	uploadAt := strings.Index(body, "- name: Upload gosec findings to code scanning")
	if scanAt < 0 || uploadAt < scanAt {
		t.Fatal("fork-safe SARIF condition can bypass scanner execution")
	}
}

func TestSecurityWorkflowUploadsTheScannerReports(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/security.yaml")
	requireWorkflowText(t, body,
		"- name: Upload scanner reports\n        if: ${{ always() }}",
		"path: ${{ runner.temp }}/security/",
		"if-no-files-found: ignore",
	)
	if !strings.Contains(body, "--report-dir=\"$RUNNER_TEMP/security\"") {
		t.Fatal("the uploaded directory is not the one the scanners write to")
	}
}

func TestSecurityWorkflowPinsExternalActions(t *testing.T) {
	pinned := regexp.MustCompile(`^[0-9a-f]{40}$`)
	steps := workflowUsesSteps(t, "security.yaml")
	for _, step := range steps {
		_, ref, ok := strings.Cut(step.uses, "@")
		if !ok || !pinned.MatchString(ref) {
			t.Errorf("%s: external action is not pinned to a full commit SHA: %s", step.where(), step.uses)
		}
	}
	if len(steps) != 9 {
		t.Fatalf("external action uses = %d, want 9", len(steps))
	}
}

func TestReleaseCheckoutsNeverPersistCredentials(t *testing.T) {
	if requireCheckoutsDropCredentials(t, "release.yaml") == 0 {
		t.Fatal("release workflow has no checkout steps")
	}
}

func TestEveryWorkflowPinsActionsByCommitSHA(t *testing.T) {
	table := actionPinTable(t)
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	names := workflowFileNames(t)
	if len(names) < 4 {
		t.Fatalf("workflows scanned = %d, want every file under .github/workflows", len(names))
	}
	used := map[string]bool{}
	for _, name := range names {
		for _, step := range workflowUsesSteps(t, name) {
			if strings.HasPrefix(step.uses, "./") {
				continue
			}
			_, ref, ok := strings.Cut(step.uses, "@")
			if !ok || !sha.MatchString(ref) {
				t.Errorf("%s: action is not pinned to a full commit SHA: %s", step.where(), step.uses)
				continue
			}
			want, listed := table[step.uses]
			if !listed {
				t.Errorf("%s: %s is not in .github/action-pins.txt", step.where(), step.uses)
				continue
			}
			used[step.uses] = true
			if step.comment != "# "+want {
				t.Errorf("%s: version comment is %q, want %q", step.where(), step.comment, "# "+want)
			}
		}
	}
	for action := range table {
		if !used[action] {
			t.Errorf("action-pins.txt lists %s, which no workflow uses", action)
		}
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
		"cef3d45478670f750782eb8f4df38ae30cdaf360:pkg/store/argon2.go:generic-api-key:17",
	}, "\n")
	if ignore != want {
		t.Fatalf("gitleaks history exceptions are not the exact fingerprint set: %q", ignore)
	}
}
