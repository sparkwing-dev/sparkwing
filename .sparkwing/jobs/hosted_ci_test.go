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

func TestHostedCIProvesThePostgresSuitesRanAgainstARealDatabase(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/ci.yaml")
	job := workflowJob(t, body, "postgres")
	requireWorkflowText(t, job,
		"image: postgres:17",
		`SPARKWING_REQUIRE_PG: "1"`,
		"SPARKWING_TEST_PG_URL: postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable",
		`--health-cmd "pg_isready -U postgres"`,
		"go test ./pkg/store ./internal/backend ./internal/orchestrator",
		"go test -v -count=1 -run 'Postgres|Pg' ./pkg/store ./internal/backend ./internal/orchestrator",
		`grep -q -- '--- SKIP' "$RUNNER_TEMP/postgres-gated.log"`,
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1",
		"actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0",
	)
	proveAt := strings.Index(job, "- name: Prove the Postgres-gated tests did not skip")
	failAt := strings.Index(job, "exit 1")
	if proveAt < 0 || failAt < proveAt {
		t.Fatal("the lane reports a Postgres skip without failing on it")
	}
}

func TestCanonicalWorkflowRunsTheCheckedOutEventChange(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	requireWorkflowText(t, body,
		"  workflow_call:\n",
		"permissions:\n  contents: read\n",
		"gate: [pre-commit, pre-push]",
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
		"run pre-commit",
		"run pre-push",
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
	prePushAt := strings.Index(body, `"$RUNNER_TEMP/sparkwing" run pre-push`)
	if stageAt < 0 || regenAt < stageAt || copyAt < regenAt || hashAt < copyAt || prePushAt < hashAt {
		t.Fatal("release self-pin snapshot is not regenerated before fingerprinting and pre-push")
	}
}

func TestCanonicalPreCommitOwnsDashboardDependencyInstallation(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	install := "- name: Install dashboard dependencies\n        if: matrix.gate == 'pre-commit'\n        run: npm ci --ignore-scripts --prefix web"
	requireWorkflowText(t, body, install)
	if got := strings.Count(body, "npm ci --ignore-scripts --prefix web"); got != 1 {
		t.Fatalf("dashboard dependency install count = %d, want 1", got)
	}
	installAt := strings.Index(body, install)
	gateAt := strings.Index(body, "- name: Run canonical pre-commit")
	if gateAt < 0 || installAt > gateAt {
		t.Fatal("dashboard dependencies are not installed before canonical pre-commit")
	}
	if strings.Contains(body, "npm --prefix web run lint") {
		t.Fatal("hosted workflow bypasses the canonical frontend-lint step")
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
		`echo "source_sha=$source_sha" >>"$GITHUB_OUTPUT"`,
	)
	requireWorkflowText(t, workflowJob(t, body, "canonical"),
		"needs: validate-tag",
		"uses: ./.github/workflows/canonical-gates.yaml",
		"source_ref: ${{ needs.validate-tag.outputs.source_sha }}",
		"release_tag: ${{ inputs.tag || github.ref_name }}",
		"contents: read",
	)
	requireWorkflowText(t, workflowJob(t, body, "security"),
		"needs: validate-tag",
		"uses: ./.github/workflows/security.yaml",
		"source_ref: ${{ needs.validate-tag.outputs.source_sha }}",
		"security-events: write",
	)
	requireWorkflowText(t, workflowJob(t, body, "build"),
		"needs: [validate-tag, canonical, security]",
		"needs.canonical.result == 'success'",
		"needs.security.result == 'success'",
		"ref: ${{ needs.validate-tag.outputs.source_sha }}",
		"path: .release-tools",
		".release-tools/bin/check-release-binary-vulnerabilities.sh",
		`go-version: "1.26.6"`,
	)
	requireWorkflowText(t, workflowJob(t, body, "build-images"),
		"needs: [validate-tag, canonical, security]",
		"needs.canonical.result == 'success'",
		"needs.security.result == 'success'",
		"contents: read",
		"packages: write",
		"persist-credentials: false",
		"ref: ${{ needs.validate-tag.outputs.source_sha }}",
		"Checkout current image recipe",
		"cp .release-tools/.dockerignore .dockerignore",
		`go-version: "1.26.6"`,
	)
	requireWorkflowText(t, workflowJob(t, body, "publish-images"),
		"needs: [validate-tag, build-images, prepare-binaries]",
		"contents: read",
		"id-token: write",
		"packages: write",
		"Verify release tag before signing images",
		"EXPECTED_SHA: ${{ needs.validate-tag.outputs.source_sha }}",
	)
	requireWorkflowText(t, workflowJob(t, body, "release"),
		"inputs.publish_images == false",
		"contents: write",
		"persist-credentials: false",
		"ref: ${{ needs.validate-tag.outputs.source_sha }}",
		"Verify release tag remains pinned",
		`test "$actual_sha" = "$EXPECTED_SHA"`,
		`go-version: "1.26.6"`,
		"TAG: ${{ inputs.tag || github.ref_name }}",
	)
	requireWorkflowText(t, workflowJob(t, body, "prepare-binaries"),
		"always() && needs.build.result == 'success'",
	)

	publishImages := workflowJob(t, body, "publish-images")
	checkAt := strings.Index(publishImages, "Verify release tag before signing images")
	signAt := strings.Index(publishImages, "Sign scanned image digests")
	tagAt := strings.Index(publishImages, "Publish scanned image tags")
	tagCheckAt := strings.Index(publishImages[tagAt:], `test "$actual_sha" = "$EXPECTED_SHA"`)
	if tagCheckAt >= 0 {
		tagCheckAt += tagAt
	}
	tagMutationAt := strings.Index(publishImages, "bash bin/publish-image-tags.sh")
	if checkAt < 0 || signAt < 0 || tagAt < 0 || checkAt > signAt || tagCheckAt < tagAt || tagMutationAt < tagCheckAt {
		t.Fatal("image signing and tagging are not guarded by tag stability checks")
	}
	releaseJob := workflowJob(t, body, "release")
	initialCheckAt := strings.Index(releaseJob, `test "$actual_sha" = "$EXPECTED_SHA"`)
	draftAt := strings.Index(releaseJob, `gh release create "$tag"`)
	uploadAt := strings.Index(releaseJob, `gh release upload "$tag" dist/*`)
	finalCheckAt := strings.LastIndex(releaseJob, `test "$actual_sha" = "${{ needs.validate-tag.outputs.source_sha }}"`)
	publishAt := strings.Index(releaseJob, `gh release edit "$tag" --draft=false`)
	if initialCheckAt < 0 || draftAt < initialCheckAt || uploadAt < draftAt || finalCheckAt < uploadAt || publishAt < finalCheckAt {
		t.Fatal("draft publication is not guarded by a final tag stability check")
	}

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

func TestReleaseRefusesToMoveAPublishedImageTag(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/release.yaml")
	requireWorkflowText(t, body, "      force_retag:\n", "        type: boolean\n")

	publish := workflowJob(t, body, "publish-images")
	requireWorkflowText(t, publish,
		"FORCE_RETAG: ${{ inputs.force_retag || false }}",
		"TAG: ${{ inputs.tag || github.ref_name }}",
		"bash bin/publish-image-tags.sh",
		"image-digests/image-digests.json",
		"name: release-image-digests",
	)
	checkoutAt := strings.Index(publish, "uses: actions/checkout@")
	downloadDigestsAt := strings.Index(publish, "uses: actions/download-artifact@")
	guardAt := strings.Index(publish, "bash bin/publish-image-tags.sh")
	if checkoutAt < 0 || downloadDigestsAt < checkoutAt || guardAt < downloadDigestsAt {
		t.Fatal("the guarded publish script is not checked out before it runs")
	}
	if strings.Contains(publish, "docker buildx imagetools") {
		t.Fatal("tag mutation is inline again, so the shell is untested")
	}

	release := workflowJob(t, body, "release")
	requireWorkflowText(t, release,
		"- name: Download published image digests",
		"if: ${{ needs.publish-images.result == 'success' }}",
		"name: release-image-digests",
		"path: dist",
		"go run ./cmd/sign-manifest -in dist/image-digests.json -out dist/image-digests.json.sig",
	)
	downloadAt := strings.Index(release, "- name: Download published image digests")
	signAt := strings.Index(release, "go run ./cmd/sign-manifest -in dist/image-digests.json")
	uploadAt := strings.Index(release, `gh release upload "$tag" dist/*`)
	if downloadAt < 0 || signAt < downloadAt || uploadAt < signAt {
		t.Fatal("the image digests asset is not signed between download and upload")
	}
}
