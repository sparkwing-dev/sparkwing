package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
		"go test -short -timeout 20m -skip 'Postgres|Pg|BackupRestoreDrill' ./internal/store ./internal/backend ./internal/orchestrator",
		"go test -v -count=1 -run 'Postgres|Pg|BackupRestoreDrill|^TestCLITokenMintUsesCommittedMembershipAuthority$|^TestRunFiltersTreatSQLMetacharactersAsLiteral$|^TestSecretMutationsTreatSQLMetacharactersAsLiteral$' ./internal/store ./internal/backend ./internal/orchestrator ./internal/controller",
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
		`gate: ${{ (inputs.source_ref != '' || inputs.release_tag != '') && fromJSON('["gate", "pre-release"]') || fromJSON('["gate", "race-touched", "pre-release", `,
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
		"gate_selection=(--sw-only gate)",
		"run gate --sw-only race-touched",
		"- name: Stage the event change\n        if: matrix.gate == 'gate' || matrix.gate == 'race-touched'",
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

func TestCanonicalSourceOverrideRetainsItsWholeGateGraph(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")), &doc); err != nil {
		t.Fatal(err)
	}
	steps := mappingValue(mappingValue(mappingValue(&doc, "jobs"), "gate"), "steps")
	for _, step := range steps.Content {
		name := mappingValue(step, "name")
		if name == nil || name.Value != "Run canonical gate" {
			continue
		}
		for _, tc := range []struct {
			source, tag string
		}{
			{},
			{source: "v0.68.2"},
			{tag: "v0.68.2"},
		} {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "sparkwing"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", mappingValue(step, "run").Value)
			cmd.Env = append(os.Environ(), "RUNNER_TEMP="+dir, "SOURCE_REF="+tc.source, "RELEASE_TAG="+tc.tag)
			got, err := cmd.Output()
			want := []string{"run", "gate"}
			if tc.source == "" && tc.tag == "" {
				want = append(want, "--sw-only", "gate")
			}
			want = append(want, "--sw-run-handle-file", filepath.Join(dir, "canonical-run.json"))
			if err != nil || !slices.Equal(strings.Fields(string(got)), want) {
				t.Errorf("source=%q tag=%q args=%s err=%v, want %v", tc.source, tc.tag, got, err, want)
			}
		}
		return
	}
	t.Fatal("canonical workflow has no broad gate")
}

func TestCanonicalRequiredGateRejectsMissingOrFailedChecks(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	jobs := mappingValue(&doc, "jobs")
	checks := mappingValue(jobs, "gate")
	if got := mappingValue(checks, "name"); got == nil || got.Value != "Canonical / ${{ matrix.gate == 'gate' && 'gate-core' || matrix.gate }}" {
		t.Fatal("matrix jobs must leave the required Canonical / gate name to the aggregate")
	}
	gate := mappingValue(jobs, "required-gate")
	for key, want := range map[string]string{"name": "Canonical / gate", "needs": "gate", "if": "${{ always() }}"} {
		if got := mappingValue(gate, key); got == nil || got.Value != want {
			t.Fatalf("required gate %s = %v, want %s", key, got, want)
		}
	}
	steps := mappingValue(gate, "steps")
	if steps == nil || len(steps.Content) != 1 {
		t.Fatal("required gate must check the combined matrix result")
	}
	step := steps.Content[0]
	result := mappingValue(mappingValue(step, "env"), "RESULT")
	command := mappingValue(step, "run")
	if result == nil || result.Value != "${{ needs.gate.result }}" || command == nil {
		t.Fatal("required gate does not consume the combined matrix result")
	}
	for _, result := range []string{"success", "failure", "cancelled", "skipped", ""} {
		cmd := exec.Command("bash", "-c", command.Value)
		cmd.Env = append(os.Environ(), "RESULT="+result)
		if err := cmd.Run(); (err == nil) != (result == "success") {
			t.Errorf("required gate result %q: %v", result, err)
		}
	}
}

func TestCanonicalBroadGateOwnsDashboardDependencyInstallation(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	install := "- name: Install dashboard dependencies\n        if: matrix.gate == 'gate'\n        run: pnpm install --frozen-lockfile --ignore-scripts --dir web"
	requireWorkflowText(t, body, install)
	if got := strings.Count(body, "pnpm install --frozen-lockfile --ignore-scripts --dir web"); got != 1 {
		t.Fatalf("dashboard dependency install count = %d, want 1", got)
	}
	installAt := strings.Index(body, install)
	gateAt := strings.Index(body, "- name: Run canonical gate")
	if gateAt < 0 || installAt > gateAt {
		t.Fatal("dashboard dependencies are not installed before the canonical broad gate")
	}
	if strings.Contains(body, "pnpm --dir web run lint") {
		t.Fatal("hosted workflow bypasses the canonical frontend-lint step")
	}
}

func TestCanonicalPreReleaseUsesItsPostgresService(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	job := mappingValue(mappingValue(&doc, "jobs"), "gate")
	image := mappingValue(mappingValue(mappingValue(job, "services"), "postgres"), "image")
	if image == nil || image.Value != "${{ (matrix.gate == 'pre-release' || startsWith(matrix.gate, 'race-store-')) && 'postgres:17' || '' }}" {
		t.Fatal("PostgreSQL service must start only for pre-release and the store race lanes")
	}
	if mappingValue(mappingValue(job, "env"), "SPARKWING_TEST_PG_URL") != nil {
		t.Fatal("PostgreSQL URL must be scoped to the pre-release step")
	}
	steps := mappingValue(job, "steps")
	if steps == nil {
		t.Fatal("canonical workflow has no steps")
	}
	found := map[string]bool{}
	for _, step := range steps.Content {
		name := mappingValue(step, "name")
		url := mappingValue(mappingValue(step, "env"), "SPARKWING_TEST_PG_URL")
		if name != nil && (name.Value == "Run canonical pre-release" || name.Value == "Run canonical store race part") {
			found[name.Value] = true
			if url == nil || url.Value != "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable" {
				t.Fatalf("%s does not address its PostgreSQL service", name.Value)
			}
			// safety: without a URL the PostgreSQL store tests skip under
			// race and the lane still passes.
			if require := mappingValue(mappingValue(step, "env"), "SPARKWING_REQUIRE_PG"); name.Value == "Run canonical store race part" && (require == nil || require.Value != "1") {
				t.Fatal("store race lanes must refuse to run without PostgreSQL")
			}
		} else if url != nil {
			t.Fatal("PostgreSQL URL escaped the pre-release and store race steps")
		}
	}
	if len(found) != 2 {
		t.Fatalf("canonical workflow PostgreSQL steps = %v, want pre-release and the store race part", found)
	}
}

func TestCanonicalWorkflowLeavesRoomAroundDeclaredDeadlines(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	minutesNode := mappingValue(mappingValue(mappingValue(&doc, "jobs"), "gate"), "timeout-minutes")
	if minutesNode == nil {
		t.Fatal("canonical gate job declares no workflow timeout")
	}
	want := fmt.Sprintf("${{ (matrix.gate == 'pre-release' || startsWith(matrix.gate, 'race-store-')) && %d || %d }}",
		int((preReleaseRunTimeout+10*time.Minute)/time.Minute),
		int((gateRunTimeout+10*time.Minute)/time.Minute))
	if minutesNode.Value != want {
		t.Fatalf("canonical workflow timeout = %q, want %q to allow setup and cleanup", minutesNode.Value, want)
	}
}

func TestCanonicalWorkflowPrintsStoredDiagnosticsAfterFailure(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	requireWorkflowText(t, body,
		`--sw-run-handle-file "$RUNNER_TEMP/canonical-run.json"`,
		"- name: Print failed canonical run diagnostics\n        if: ${{ failure() }}\n        continue-on-error: true\n        timeout-minutes: 2",
		`bash "$reporter" "$RUNNER_TEMP/canonical-run.json" "$RUNNER_TEMP/sparkwing"`,
	)
	if got := strings.Count(body, `--sw-run-handle-file "$RUNNER_TEMP/canonical-run.json"`); got != 4 {
		t.Fatalf("canonical run-handle publication count = %d, want broad gate, race, pre-release and store race part", got)
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

// safety: the store race is split across hosted lanes only because every part
// runs somewhere; a lane dropped from the matrix would ship a store race
// nothing looked at.
func TestCanonicalStoreRacePartsCoverEveryPartOnce(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")), &doc); err != nil {
		t.Fatal(err)
	}
	job := mappingValue(mappingValue(&doc, "jobs"), "gate")
	matrix := mappingValue(mappingValue(mappingValue(job, "strategy"), "matrix"), "gate").Value
	at := strings.LastIndex(matrix, "fromJSON('")
	end := strings.LastIndex(matrix, "')")
	if at < 0 || end < at {
		t.Fatalf("matrix %q has no normal lane list", matrix)
	}
	var lanes []string
	if err := json.Unmarshal([]byte(matrix[at+len("fromJSON('"):end]), &lanes); err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{}
	for _, step := range mappingValue(job, "steps").Content {
		if name := mappingValue(step, "name"); name != nil {
			if run := mappingValue(step, "run"); run != nil {
				scripts[name.Value] = run.Value
			}
		}
	}
	run := func(script string, env ...string) []string {
		t.Helper()
		dir := t.TempDir()
		for path, body := range map[string]string{
			"sparkwing":                      "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$RUNNER_TEMP/args\"\n",
			"git":                            "#!/bin/sh\necho 0123456789abcdef0123456789abcdef01234567\n",
			"bin/check-hosted-gate-clean.sh": "exit 0\n",
		} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("bash", "-c", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), append([]string{"RUNNER_TEMP=" + dir, "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")}, env...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("step failed: %v\n%s", err, out)
		}
		args, err := os.ReadFile(filepath.Join(dir, "args"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Fields(string(args))
	}
	partOf := func(args []string) raceStorePart {
		t.Helper()
		at := slices.Index(args, "--race-store-part")
		if at < 1 || at+2 != len(args) || args[at-1] != "--" {
			t.Fatalf("args %v do not end with -- --race-store-part k/n", args)
		}
		part, err := parseRaceStorePart(args[at+1])
		if err != nil {
			t.Fatal(err)
		}
		return part
	}

	preRelease := scripts["Run canonical pre-release"]
	if args := run(preRelease, "SOURCE_REF=", "RELEASE_TAG="); !slices.Equal(args[len(args)-3:], []string{"--", "--race-store-part", raceStorePartsElsewhere}) {
		t.Fatalf("pre-release args %v, want the store race left to its lanes", args)
	}
	var parts []raceStorePart
	if args := run(preRelease, "SOURCE_REF=v0.68.2", "RELEASE_TAG="); slices.Contains(args, "--race-store-part") {
		t.Fatalf("source override %v must race every store shard in its one pre-release job", args)
	}
	for _, lane := range lanes {
		if !strings.HasPrefix(lane, "race-store-") {
			continue
		}
		args := run(scripts["Run canonical store race part"], "LANE="+lane)
		if !slices.Equal(args[:5], []string{"run", "pre-release", "--sw-start-at", "race-store", "--sw-stop-at"}) || args[5] != "race-store" {
			t.Fatalf("lane %s args %v, want only the race-store step", lane, args)
		}
		parts = append(parts, partOf(args))
	}
	if len(parts) == 0 {
		t.Fatal("no race-store lane")
	}
	count := parts[0].count
	seen := map[int]bool{}
	for _, part := range parts {
		if part.count != count || seen[part.index] {
			t.Fatalf("parts %v disagree on the count or repeat one", parts)
		}
		seen[part.index] = true
	}
	if len(seen) != count || count < 2 {
		t.Fatalf("parts %v do not cover 1..%d", parts, count)
	}
}
