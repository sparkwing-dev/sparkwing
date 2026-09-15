package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/k8s"
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

func TestReleaseWorkflowUsesTheRunnerImageContract(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../.github/workflows/release.yaml")
	if err != nil {
		t.Fatal(err)
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
		"FROM " + alpineImage + " AS runtime",
		"ARG SPARKWING_IMAGE_REFRESH=local",
		"RUN test -n \"${SPARKWING_IMAGE_REFRESH}\" && apk upgrade --no-cache && apk add --no-cache ca-certificates git git-daemon openssh-client procps-ng",
		"COPY --from=" + goImage + " /usr/local/go /usr/local/go",
		"COPY build/runner-entrypoint.sh /usr/local/bin/runner-entrypoint.sh",
		"COPY --from=build /out/" + k8s.JobBinary + " /usr/local/bin/" + k8s.JobBinary,
		`ENTRYPOINT ["/usr/local/bin/runner-entrypoint.sh"]`,
		`CMD ["/usr/local/bin/` + k8s.JobBinary + `"]`,
	} {
		if !containsDockerInstruction(instructions, required) {
			t.Errorf("runner image contract missing %q", required)
		}
	}
	if !strings.Contains(string(runnerDockerfile), "-o /out/"+k8s.JobBinary) {
		t.Errorf("runner image does not build %s, the binary the Kubernetes Job fallback invokes", k8s.JobBinary)
	}
	if !strings.Contains(string(body), `go-version: "`+goVersion+`"`) {
		t.Errorf("release workflow Go version does not match runner toolchain %s", goVersion)
	}
}

func TestReleaseWorkflowRefreshesAlpinePackagesPerAttempt(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../.github/workflows/release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `SPARKWING_IMAGE_REFRESH: ${{ github.run_id }}-${{ github.run_attempt }}`) {
		t.Error("release workflow does not refresh Alpine packages for each image build attempt")
	}

	for _, path := range []string{"../../build/Dockerfile.binary", "../../build/Dockerfile.runner"} {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			dockerfile, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			instructions := dockerfileInstructions(dockerfile)
			if !containsDockerInstruction(instructions, "ARG SPARKWING_IMAGE_REFRESH=local") {
				t.Error("image refresh contract does not declare SPARKWING_IMAGE_REFRESH")
			}
			const refresh = "RUN test -n \"${SPARKWING_IMAGE_REFRESH}\" && apk upgrade --no-cache"
			var found bool
			for _, instruction := range instructions {
				found = found || strings.HasPrefix(instruction, refresh)
			}
			if !found {
				t.Errorf("image refresh contract missing %q", refresh)
			}
		})
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
