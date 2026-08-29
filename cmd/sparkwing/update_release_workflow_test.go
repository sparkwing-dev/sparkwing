package main

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseWorkflowPublishesImmutableSignedUpdaterAssets(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../.github/workflows/release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	for _, required := range []string{
		"SHA256SUMS.sig",
		"sparkwing-*.sig",
		"SPARKWING_RELEASE_SIGNING_KEY",
		"verify-release",
		"--draft",
		"--verify-tag",
		"--latest=false",
		"gh release edit \"$tag\" --draft=false",
		"--json isDraft",
		"gh release delete \"$tag\" --yes",
		"trap cleanup_draft EXIT",
		"group: release-${{ inputs.tag || github.ref_name }}",
		"if [ \"$state\" = true ]",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow missing %q", required)
		}
	}
	if strings.Contains(workflow, "existing release $tag; updating assets + notes") {
		t.Error("release workflow mutates an existing public release")
	}
	if strings.Contains(workflow, "gh release upload \"$tag\" --clobber") {
		t.Error("release workflow permits signed assets to be overwritten")
	}
}

func TestReleaseWorkflowScansArtifactsBeforePublication(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../.github/workflows/release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	for _, required := range []string{
		"npm --prefix web audit --omit=dev --audit-level=high",
		"check-release-binary-vulnerabilities.sh",
		"push-by-digest=true,name-canonical=true,push=true",
		"check-vulnerability-waivers.go .trivyignore.yaml",
		"Scan image digest (linux/amd64)",
		"Scan image digest (linux/arm64)",
		"TRIVY_PLATFORM: linux/amd64",
		"TRIVY_PLATFORM: linux/arm64",
		"severity: HIGH,CRITICAL",
		"exit-code: \"1\"",
		"aquasecurity/trivy-action@ed142fd0673e97e23eac54620cfb913e5ce36c25 # v0.36.0",
		"needs: [validate-tag, prepare-binaries, publish-images]",
		"needs: [validate-tag, build-images, prepare-binaries]",
		"name: scanned-release-binaries",
		"pattern: scanned-image-*",
		"Publish scanned image tags",
		"Build dashboard for image",
		"Audit production dashboard dependencies for image",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow missing artifact vulnerability gate %q", required)
		}
	}
	if scan, publish := strings.Index(workflow, "Scan image digest (linux/amd64)"), strings.Index(workflow, "Publish scanned image tags"); scan < 0 || publish < 0 || scan > publish {
		t.Error("release workflow publishes image tags before vulnerability scanning")
	}
	buildImages, publishImages := strings.Index(workflow, "\n  build-images:"), strings.Index(workflow, "\n  publish-images:")
	if buildImages < 0 || publishImages < 0 || buildImages > publishImages {
		t.Fatal("release workflow is missing ordered build-images and publish-images jobs")
	}
	if strings.Contains(workflow[buildImages:publishImages], "Publish scanned image tags") {
		t.Error("an individual image matrix cell can publish tags before the complete scan matrix passes")
	}
	imageJob := workflow[buildImages:publishImages]
	dashboardAudit := strings.Index(imageJob, "Audit production dashboard dependencies for image")
	dashboardBuild := strings.Index(imageJob, "Build dashboard for image")
	imageBuild := strings.Index(imageJob, "Build and push image")
	if dashboardAudit < 0 || dashboardBuild < dashboardAudit || imageBuild < dashboardBuild {
		t.Error("sparkwing-web image build does not audit and populate the embedded dashboard first")
	}
	if strings.Contains(workflow, "aquasecurity/trivy-action@v0.36.0") {
		t.Error("release workflow executes Trivy from a mutable tag")
	}
	binaryDashboardAudit := strings.Index(workflow, "Audit production dashboard dependencies\n")
	binaryDashboardBuild := strings.Index(workflow, "Build dashboard + populate next-out")
	binaryBuild := strings.Index(workflow, "Build ${{ matrix.binary }}")
	if binaryDashboardAudit < 0 || binaryDashboardBuild < binaryDashboardAudit || binaryBuild < binaryDashboardBuild {
		t.Error("release binary build does not audit and populate the embedded dashboard first")
	}
	scan, upload := strings.Index(workflow, "Scan exact release binary"), strings.Index(workflow, "Upload binary artifact")
	if scan < 0 || upload < 0 || scan > upload {
		t.Error("release workflow uploads binaries before vulnerability scanning")
	} else {
		scanStep := workflow[scan:upload]
		if strings.Contains(scanStep, "\n          GOOS:") || strings.Contains(scanStep, "\n          GOARCH:") {
			t.Error("release workflow cross-compiles the host govulncheck scanner")
		}
	}
	rcodesign := strings.Index(workflow, "Ad-hoc-sign macOS binaries (rcodesign)")
	assembled := strings.Index(workflow, "Scan assembled release binaries")
	checksums := strings.Index(workflow, "Compute SHA256SUMS")
	if rcodesign < 0 || assembled < rcodesign || checksums < assembled {
		t.Error("release workflow does not scan the final rcodesign-mutated binary bytes before checksums and publication")
	}
}

func TestReleaseWorkflowUsesTheRunnerImageContract(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../.github/workflows/release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	const dockerfileSelection = "file: ${{ matrix.binary == 'sparkwing-runner' && 'build/Dockerfile.runner' || 'build/Dockerfile.binary' }}"
	if !strings.Contains(string(body), dockerfileSelection) {
		t.Fatalf("release workflow does not select the dedicated runner Dockerfile:\nwant %s", dockerfileSelection)
	}
	runnerDockerfile, err := os.ReadFile("../../build/Dockerfile.runner")
	if err != nil {
		t.Fatal(err)
	}
	instructions := dockerfileInstructions(runnerDockerfile)
	const goVersion = "1.26.6"
	const goImage = "golang:" + goVersion + "-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83"
	const alpineImage = "alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b"
	for _, required := range []string{
		"FROM --platform=$BUILDPLATFORM " + goImage + " AS build",
		"FROM " + alpineImage,
		"RUN apk upgrade --no-cache && apk add --no-cache ca-certificates git git-daemon openssh-client",
		"COPY --from=" + goImage + " /usr/local/go /usr/local/go",
		"COPY build/runner-entrypoint.sh /usr/local/bin/runner-entrypoint.sh",
		"COPY --from=build /out/sparkwing-runner /usr/local/bin/sparkwing-runner",
		`ENTRYPOINT ["/usr/local/bin/runner-entrypoint.sh"]`,
		`CMD ["/usr/local/bin/sparkwing-runner"]`,
	} {
		if !containsDockerInstruction(instructions, required) {
			t.Errorf("runner image contract missing %q", required)
		}
	}
	if !strings.Contains(string(body), `go-version: "`+goVersion+`"`) {
		t.Errorf("release workflow Go version does not match runner toolchain %s", goVersion)
	}
}

func dockerfileInstructions(body []byte) []string {
	logical := strings.ReplaceAll(string(body), "\\\n", " ")
	var instructions []string
	for _, line := range strings.Split(logical, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		instructions = append(instructions, line)
	}
	return instructions
}

func containsDockerInstruction(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
