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

func releaseQueueContract(body string) error {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		return err
	}
	jobs := mappingValue(&doc, "jobs")
	for _, name := range []string{"release", "release-index"} {
		job := mappingValue(jobs, name)
		if job == nil && name == "release-index" {
			continue
		}
		publication := mappingValue(job, "concurrency")
		for _, field := range []struct{ key, value, tag string }{
			{"group", "release-publication", "!!str"},
			{"queue", "max", "!!str"},
			{"cancel-in-progress", "false", "!!bool"},
		} {
			node := mappingValue(publication, field.key)
			if node == nil || node.Kind != yaml.ScalarNode || node.Value != field.value || node.Tag != field.tag {
				return fmt.Errorf("%s publication concurrency requires %s: %s", name, field.key, field.value)
			}
		}
	}

	if mappingValue(mappingValue(&doc, "concurrency"), "queue") != nil {
		return fmt.Errorf("the queue compatibility exception covers only the release publication job")
	}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		if jobs.Content[i].Value != "release" && jobs.Content[i].Value != "release-index" && mappingValue(mappingValue(jobs.Content[i+1], "concurrency"), "queue") != nil {
			return fmt.Errorf("the queue compatibility exception does not cover job %s", jobs.Content[i].Value)
		}
	}
	return nil
}

func TestReleasePublicationRetainsItsSerializedQueue(t *testing.T) {
	if err := releaseQueueContract(readHostedCIFile(t, ".github/workflows/release.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseQueueContractRefusesUnsafeOrUncoveredConfiguration(t *testing.T) {
	const valid = "jobs:\n  release:\n    concurrency:\n      group: release-publication\n      queue: max\n      cancel-in-progress: false\n"
	if err := releaseQueueContract(valid); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"missing queue":  strings.Replace(valid, "      queue: max\n", "", 1),
		"unknown queue":  strings.Replace(valid, "queue: max", "queue: banana", 1),
		"numeric queue":  strings.Replace(valid, "queue: max", "queue: 1", 1),
		"cancel running": strings.Replace(valid, "cancel-in-progress: false", "cancel-in-progress: true", 1),
		"quoted false":   strings.Replace(valid, "cancel-in-progress: false", "cancel-in-progress: 'false'", 1),
		"per-tag group":  strings.Replace(valid, "group: release-publication", "group: release-${{ github.ref_name }}", 1),
		"workflow queue": valid + "concurrency: {group: other, queue: max}\n",
		"other job":      valid + "  other:\n    concurrency: {group: other, queue: max}\n",
		"invalid YAML":   "jobs: [",
	} {
		t.Run(name, func(t *testing.T) {
			if err := releaseQueueContract(body); err == nil {
				t.Fatal("unsafe or unvalidated queue configuration passed")
			}
		})
	}
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
	if !strings.Contains(body, "run security-scan -- --report-dir=\"$RUNNER_TEMP/security\"") {
		t.Fatal("the uploaded directory is not the one the scanners write to, or the flag precedes the `--` " +
			"that separates pipeline arguments from run flags")
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
	if len(steps) != 10 {
		t.Fatalf("external action uses = %d, want 10", len(steps))
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

func TestGitleaksExclusionsCannotHideRepositoryPaths(t *testing.T) {
	body := readHostedCIFile(t, ".gitleaks.toml")
	sections := strings.Split(body, "[[rules]]")
	if strings.Contains(sections[0], "paths =") {
		t.Fatal("global gitleaks allow-list excludes a repository path")
	}
	for _, section := range sections[1:] {
		blocks := strings.Split(section, "[[rules.allowlists]]")
		rule := strings.TrimSpace(blocks[0])
		if rule != `id = "generic-api-key"` && rule != `id = "private-key"` && rule != `id = "curl-auth-header"` {
			t.Fatalf("unexpected gitleaks rule override: %s", rule)
		}
		for _, block := range blocks[1:] {
			if !strings.Contains(block, `description = "`) || !strings.Contains(block, `condition = "AND"`) {
				t.Fatal("gitleaks exception needs a reason and combined path/value matching")
			}
			path := regexp.MustCompile(`(?m)^paths = \['''(.+)'''\]$`).FindStringSubmatch(block)
			if len(path) != 2 || !strings.HasPrefix(path[1], "(^|/)") || !strings.HasSuffix(path[1], "$") {
				t.Fatal("gitleaks exception needs one anchored file path")
			}
			literalPath := strings.TrimSuffix(strings.TrimPrefix(path[1], "(^|/)"), "$")
			literalPath = strings.ReplaceAll(literalPath, `\.`, ".")
			if regexp.QuoteMeta(literalPath) != strings.TrimSuffix(strings.TrimPrefix(path[1], "(^|/)"), "$") {
				t.Fatal("gitleaks exception path contains a pattern instead of one file")
			}
			values := regexp.MustCompile(`'''(.*?)'''`).FindAllStringSubmatch(strings.SplitN(block, "regexes =", 2)[1], -1)
			if len(values) == 0 {
				t.Fatal("gitleaks exception needs an exact fixture value")
			}
			for _, value := range values {
				if strings.HasPrefix(value[1], `^\Q`) && strings.HasSuffix(value[1], `\E$`) {
					continue
				}
				if value[1] != "^artifacts/blobs/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855$" {
					t.Fatal("gitleaks exception does not match an exact fixture value")
				}
			}
		}
	}
	ignore := strings.TrimSpace(readHostedCIFile(t, ".gitleaksignore"))
	want := strings.Join([]string{
		"49be43e4b2e9c0dc24ee7d50eaf4a0dd0291e147:internal/web/next-out/_next/static/chunks/17mq5rf2.ibbt.js:generic-api-key:55",
		"c4f254125ffc14690be63b70e25c21c90e95932c:internal/storage/segment_artifact_test.go:generic-api-key:6",
		"cef3d45478670f750782eb8f4df38ae30cdaf360:internal/store/argon2.go:generic-api-key:17",
		"bbd6e83949b551d822df445e15ddc2ae649f3b0d:cmd/sparkwing/agents_test.go:generic-api-key:15",
		"01fe110594cc9d898a26d12f510397f4b2cd53dc:install/install.sh:generic-api-key:27",
		"26f34bc5c311eba8dad006f49f84c82ef8c115cc:internal/controller/webhook_github_bindings_test.go:generic-api-key:21",
		"307a77eba29cf80aeb16b78190aed22bbf2787d0:internal/envredact/envredact_test.go:generic-api-key:280",
		"31bd09fb7a6bcb79dd4533c3a44a702e775c5ba9:internal/envredact/envredact_test.go:generic-api-key:302",
		"31bd09fb7a6bcb79dd4533c3a44a702e775c5ba9:internal/envredact/envredact_test.go:generic-api-key:306",
		"397721c9535aa4ad4cc64a068076e9e0ffc9afc8:internal/envredact/envredact_test.go:private-key:298",
		"7c75d7dbedc7b46c71c149db7996f6c9beabfc23:cmd/sparkwing/worktree_snapshot_test.go:generic-api-key:513",
		"7c75d7dbedc7b46c71c149db7996f6c9beabfc23:cmd/sparkwing/worktree_snapshot_test.go:generic-api-key:514",
		"7c75d7dbedc7b46c71c149db7996f6c9beabfc23:cmd/sparkwing/worktree_snapshot_test.go:generic-api-key:522",
		"50470b89358f30fde06fcaa53c6b5ebefb8c2232:internal/bincache/direct_credential_test.go:private-key:15",
		"0a60cdb1e4ee34b04a177f3978a0f9ebf4684f20:refund-platform.patch:generic-api-key:144",
		"5e1de85c8d0b9fee356abea4e0b417c6ae3cc7f1:internal/controller/testdata/checkout_contract.json:generic-api-key:79",
	}, "\n")
	if ignore != want {
		t.Fatalf("gitleaks history exceptions are not the exact fingerprint set: %q", ignore)
	}
}
