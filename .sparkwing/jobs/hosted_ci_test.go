package jobs

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"
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

// safety: an integration branch joins the list, so the contract is that main
// is on it rather than that it is the only name.
func requireWorkflowPushBranches(t *testing.T, body string, want ...string) {
	t.Helper()
	_, after, ok := strings.Cut(body, "  push:\n    branches: [")
	if !ok {
		t.Fatal("hosted CI has no push trigger carrying a branch list")
	}
	list, _, ok := strings.Cut(after, "]")
	if !ok {
		t.Fatal("hosted CI push branch list is unterminated")
	}
	have := strings.Split(list, ",")
	for i := range have {
		have[i] = strings.TrimSpace(have[i])
	}
	for _, name := range want {
		if !slices.Contains(have, name) {
			t.Errorf("hosted CI push branches = %v, want %s among them", have, name)
		}
	}
}

func TestHostedCITriggersCanonicalReadOnlyChecks(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/ci.yaml")
	requireWorkflowText(t, body,
		"  pull_request:\n",
		"permissions:\n  contents: read\n",
		"uses: ./.github/workflows/canonical-gates.yaml",
	)
	requireWorkflowPushBranches(t, body, "main")
	if strings.Contains(body, ": write") {
		t.Fatal("hosted CI grants a write permission")
	}
}

func TestHostedCIProvesThePostgresSuitesRanAgainstARealDatabase(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/ci.yaml")
	job := workflowJob(t, body, "postgres")
	requireWorkflowText(t, job,
		"image: postgres:17",
		`SPARKWING_REQUIRE_PG: "1"`,
		"SPARKWING_TEST_PG_URL: postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable",
		`--health-cmd "pg_isready -U postgres"`,
		"go test ./pkg/store ./internal/backend ./internal/orchestrator",
		"go test -v -count=1 -run 'Postgres|Pg|BackupRestoreDrill' ./pkg/store ./internal/backend ./internal/orchestrator",
		`grep -q -- '--- SKIP' "$RUNNER_TEMP/postgres-gated.log"`,
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1",
		"actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0",
	)
	proveAt := strings.Index(job, "- name: Prove the Postgres-gated tests did not skip")
	failAt := strings.LastIndex(job, "exit 1")
	if proveAt < 0 || failAt < proveAt {
		t.Fatal("the lane reports a Postgres skip without failing on it")
	}
}

// The backup drill dumps and restores a live database, so it needs client
// binaries at least as new as the service. Without them its Postgres
// subtest skips, and a skip is what the no-skip step exists to catch.
func TestHostedCIGivesTheBackupDrillAPostgres17Client(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/ci.yaml")
	job := workflowJob(t, body, "postgres")
	requireWorkflowText(t, job,
		"SPARKWING_PG_BIN: /usr/lib/postgresql/17/bin",
		"https://www.postgresql.org/media/keys/ACCC4CF8.asc",
		"apt-get install -y --no-install-recommends postgresql-client-17",
		`major="$("$SPARKWING_PG_BIN/pg_dump" --version | awk '{print $3}' | cut -d. -f1)"`,
		"BackupRestoreDrill",
	)
	installAt := strings.Index(job, "- name: Install a Postgres 17 client")
	versionAt := strings.Index(job, "- name: Prove the client is no older than the server")
	suitesAt := strings.Index(job, "- name: Run the Postgres conformance suites")
	if installAt < 0 || versionAt < installAt || suitesAt < versionAt {
		t.Fatal("the lane runs the Postgres suites before it has a client of a proven version")
	}
}

func TestCanonicalWorkflowRunsTheCheckedOutEventChange(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	requireWorkflowText(t, body,
		"  workflow_call:\n",
		"permissions:\n  contents: read\n",
		"gate: [gate, pre-release]",
		"persist-credentials: false",
		"Copy the immutable release verifier",
		`cp bin/check-hosted-gate-clean.sh "$RUNNER_TEMP/check-hosted-gate-clean.sh"`,
		`bash "$RUNNER_TEMP/check-hosted-gate-clean.sh" --release-self-pin`,
		"ref: ${{ inputs.source_ref || github.sha }}",
		"target_sha=\"$(git rev-parse HEAD)\"",
		"git reset --soft \"$base\"",
		"git tag --delete -- \"$RELEASE_TAG\"",
		"-dropreplace=github.com/sparkwing-dev/sparkwing",
		"pkg/scaffold/version.go",
		"run gate",
		"run pre-release",
	)
	if strings.Contains(body, ": write") {
		t.Fatal("canonical gates grant a write permission")
	}
	verifierCheckoutAt := strings.Index(body, "- name: Checkout immutable release verifier")
	verifierRefAt := strings.Index(body, "ref: ${{ github.sha }}")
	verifierCopyAt := strings.Index(body, "- name: Copy the immutable release verifier")
	sourceCheckoutAt := strings.Index(body, "ref: ${{ inputs.source_ref || github.sha }}")
	verifierRunAt := strings.Index(body, `bash "$RUNNER_TEMP/check-hosted-gate-clean.sh" --release-self-pin`)
	if verifierCheckoutAt < 0 || verifierRefAt < verifierCheckoutAt || verifierCopyAt < verifierRefAt || sourceCheckoutAt < verifierCopyAt || verifierRunAt < sourceCheckoutAt {
		t.Fatal("release verifier is not copied from the caller SHA before tagged-source checkout and execution")
	}
	regenAt := strings.Index(body, `bash bin/regen-api-snapshot.sh "$snapshot_dir"`)
	copyAt := strings.Index(body, `cp "$snapshot_dir/pkg_scaffold.txt" .apidiff/pkg_scaffold.txt`)
	stageAt := strings.Index(body, `git add -- .sparkwing/go.mod .sparkwing/go.sum`)
	hashAt := strings.Index(body, `patch_oid="$(git diff HEAD --binary -- .apidiff/pkg_scaffold.txt .sparkwing/go.mod .sparkwing/go.sum`)
	preReleaseAt := strings.Index(body, `"$RUNNER_TEMP/sparkwing" run pre-release`)
	if stageAt < 0 || regenAt < stageAt || copyAt < regenAt || hashAt < copyAt || preReleaseAt < hashAt {
		t.Fatal("release self-pin snapshot is not regenerated before fingerprinting and pre-release")
	}
}

func TestCanonicalBroadGateOwnsDashboardDependencyInstallation(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	install := "- name: Install dashboard dependencies\n        if: matrix.gate == 'gate'\n        run: npm ci --ignore-scripts --prefix web"
	requireWorkflowText(t, body, install)
	if got := strings.Count(body, "npm ci --ignore-scripts --prefix web"); got != 1 {
		t.Fatalf("dashboard dependency install count = %d, want 1", got)
	}
	installAt := strings.Index(body, install)
	gateAt := strings.Index(body, "- name: Run canonical gate")
	if gateAt < 0 || installAt > gateAt {
		t.Fatal("dashboard dependencies are not installed before the canonical broad gate")
	}
	if strings.Contains(body, "npm --prefix web run lint") {
		t.Fatal("hosted workflow bypasses the canonical frontend-lint step")
	}
}

func TestCanonicalWorkflowLeavesRoomAroundTheGateDeadline(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	minutesNode := mappingValue(mappingValue(mappingValue(&doc, "jobs"), "gate"), "timeout-minutes")
	if minutesNode == nil {
		t.Fatal("canonical gate job declares no workflow timeout")
	}
	minutes, err := strconv.Atoi(minutesNode.Value)
	if err != nil {
		t.Fatalf("canonical gate timeout-minutes = %q: %v", minutesNode.Value, err)
	}
	if room := time.Duration(minutes)*time.Minute - gateRunTimeout; room < 5*time.Minute {
		t.Fatalf("canonical workflow leaves %s around the %s gate deadline, want at least 5m for setup and cleanup", room, gateRunTimeout)
	}
}

func TestCanonicalWorkflowPrintsStoredDiagnosticsAfterFailure(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	requireWorkflowText(t, body,
		`--sw-run-handle-file "$RUNNER_TEMP/canonical-run.json"`,
		"- name: Print failed canonical run diagnostics\n        if: ${{ failure() }}\n        continue-on-error: true\n        timeout-minutes: 2",
		`bash "$reporter" "$RUNNER_TEMP/canonical-run.json" "$RUNNER_TEMP/sparkwing"`,
	)
	if got := strings.Count(body, `--sw-run-handle-file "$RUNNER_TEMP/canonical-run.json"`); got != 2 {
		t.Fatalf("canonical run-handle publication count = %d, want gate and pre-release", got)
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
		"- name: Upload dashboard browser failure artifacts\n        if: ${{ failure() && matrix.gate == 'gate' && hashFiles('web/test-results/.sparkwing-browser-failed') != '' }}\n        continue-on-error: true\n        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2\n        with:\n          name: dashboard-browser-${{ github.run_id }}-${{ github.run_attempt }}\n          path: |\n            web/test-results/\n            web/playwright-report/\n          if-no-files-found: ignore\n          retention-days: 14",
	)
}

func TestHostedMutationGuardCoversEveryGitStatus(t *testing.T) {
	body := readHostedCIFile(t, "bin/check-hosted-gate-clean.sh")
	requireWorkflowText(t, body,
		"actual_head=\"$(git rev-parse HEAD)\"",
		"git status --porcelain --untracked-files=all",
	)
}

func TestREADMEPublishesStableCIBadge(t *testing.T) {
	body := readHostedCIFile(t, "README.md")
	requireWorkflowText(t, body,
		"actions/workflows/ci.yaml/badge.svg?branch=main",
		"actions/workflows/ci.yaml)",
	)
}
