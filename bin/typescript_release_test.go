package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type sdkReleaseStep struct {
	Name  string
	Uses  string
	Run   string
	If    string
	Allow bool `yaml:"continue-on-error"`
	With  map[string]string
	Env   map[string]string
}

type sdkReleaseJob struct {
	Permissions map[string]string
	Needs       yaml.Node
	If          string
	Allow       bool `yaml:"continue-on-error"`
	Steps       []sdkReleaseStep
	Strategy    struct {
		Matrix struct {
			Include []struct{ Arch, Runner string }
		}
	}
	RunsOn string `yaml:"runs-on"`
}

func TestTypeScriptReleaseRequiresBothNativeBridges(t *testing.T) {
	body, err := os.ReadFile("../.github/workflows/release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct{ Jobs map[string]sdkReleaseJob }
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["typescript-native"]
	if job.If != "${{ !inputs.index_only && (inputs.verify_only || needs.typescript-decision.outputs.publish == 'true') }}" || job.Allow || !hasSDKDependency(job, "validate-tag") {
		t.Fatal("native verification must be required after tag resolution")
	}
	want := map[string]string{"x64": "windows-2025", "arm64": "windows-11-arm"}
	for _, target := range job.Strategy.Matrix.Include {
		if want[target.Arch] != target.Runner {
			t.Fatalf("unexpected native target: %+v", target)
		}
		delete(want, target.Arch)
	}
	if len(want) != 0 || job.RunsOn != "${{ matrix.runner }}" {
		t.Fatalf("missing native Windows targets: %v", want)
	}
	for _, step := range job.Steps {
		if step.If != "" || step.Allow {
			t.Fatalf("native step can be skipped or ignored: %s", step.Name)
		}
	}
	checkSDKStep(t, job, "Verify Windows bridge", "release:native", "--version", "--arch", "--headers", "--node-lib")
	checkSDKAction(t, job, "actions/checkout@", "ref", "${{ needs.validate-tag.outputs.source_sha }}")
	checkSDKAction(t, job, "actions/setup-node@", "architecture", "${{ matrix.arch }}")
	checkSDKAction(t, job, "actions/upload-artifact@", "path", "sdk/typescript/native/win32-${{ matrix.arch }}")
	checkSDKAction(t, job, "actions/upload-artifact@", "if-no-files-found", "error")
	assembly := workflow.Jobs["typescript-package"]
	if assembly.If != "${{ !inputs.verify_only && !inputs.index_only && needs.typescript-decision.outputs.publish == 'true' }}" || assembly.Allow || !hasSDKDependency(assembly, "typescript-native") || !hasSDKDependency(assembly, "validate-tag") {
		t.Fatal("package assembly must depend on successful native work and the resolved source")
	}
	checkSDKAction(t, assembly, "actions/checkout@", "ref", "${{ needs.validate-tag.outputs.source_sha }}")
	for _, arch := range []string{"x64", "arm64"} {
		checkSDKAction(t, assembly, "actions/download-artifact@", "path", "sdk/typescript/native/win32-"+arch)
	}
	checkSDKStep(t, assembly, "Assemble TypeScript package", "release:pack", "--version", "--destination")
	publication := workflow.Jobs["release"]
	if publication.Permissions["id-token"] != "write" {
		t.Fatal("npm trusted publishing requires id-token: write on the publication job")
	}
	checkSDKStep(t, publication, "Install npm for trusted publishing", "npm install -g npm@11.6.2", "npm --version")
	nodeReady, npmReady := false, false
	for _, step := range publication.Steps {
		if strings.HasPrefix(step.Uses, "actions/setup-node@") {
			nodeReady = true
		}
		if step.Name == "Install npm for trusted publishing" {
			if !nodeReady {
				t.Fatal("pinned npm must be installed after Node setup")
			}
			npmReady = true
		}
		if step.Name == "Publish TypeScript package" && !npmReady {
			t.Fatal("npm must support OIDC before publishing")
		}
	}
	for _, name := range []string{"dashboard"} {
		if workflow.Jobs[name].If != "${{ !inputs.verify_only && !inputs.index_only }}" {
			t.Errorf("verification-only dispatch must skip %s", name)
		}
	}
	if !hasSDKDependency(workflow.Jobs["build"], "dashboard") {
		t.Fatal("release builds must depend on the dashboard disabled during verification")
	}
	if !hasSDKDependency(publication, "typescript-package") {
		t.Fatal("publication does not depend on TypeScript package assembly")
	}
	checkSDKAction(t, publication, "actions/download-artifact@", "name", "release-typescript")
	checkSDKStep(t, publication, "Publish TypeScript package", "$RELEASE_TOOLS/bin/publish-typescript.mjs", "typescript-dist/", "$TAG")
	for i, step := range publication.Steps {
		if step.Name == "Publish complete immutable release" {
			for _, earlier := range publication.Steps[:i] {
				if earlier.Name == "Publish TypeScript package" {
					return
				}
			}
			t.Fatal("npm failure could strand an immutable GitHub release")
		}
	}
	t.Fatal("missing immutable GitHub publication")
}

func hasSDKDependency(job sdkReleaseJob, name string) bool {
	if job.Needs.Kind == yaml.ScalarNode {
		return job.Needs.Value == name
	}
	for _, dependency := range job.Needs.Content {
		if dependency.Value == name {
			return true
		}
	}
	return false
}

func checkSDKStep(t *testing.T, job sdkReleaseJob, name string, fragments ...string) {
	t.Helper()
	for _, step := range job.Steps {
		if step.Name != name {
			continue
		}
		if (step.If != "" && !(name == "Publish TypeScript package" && step.If == "needs.typescript-decision.outputs.publish == 'true'")) || step.Allow {
			t.Fatalf("required step %q can be skipped or ignored", name)
		}
		for _, fragment := range fragments {
			if !strings.Contains(step.Run, fragment) {
				t.Errorf("%s does not invoke %q", name, fragment)
			}
		}
		return
	}
	t.Fatalf("missing required step %q", name)
}

func checkSDKAction(t *testing.T, job sdkReleaseJob, prefix, key, value string) {
	t.Helper()
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, prefix) && step.With[key] == value && (step.If == "" || (value == "release-typescript" && step.If == "needs.typescript-decision.outputs.publish == 'true'")) && !step.Allow {
			return
		}
	}
	t.Fatalf("missing required %s action with %s=%q", prefix, key, value)
}

func TestTypeScriptReleaseDecision(t *testing.T) {
	command := exec.Command("node", "--test", "typescript-release.test.mjs")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}

func TestTypeScriptDecisionUsesCurrentPublicationToolsForRecovery(t *testing.T) {
	body, err := os.ReadFile("../.github/workflows/release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct{ Jobs map[string]sdkReleaseJob }
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	decision := workflow.Jobs["typescript-decision"]
	currentTools := false
	for _, step := range decision.Steps {
		if step.Name == "Checkout publication tools" {
			currentTools = step.If == "github.event_name == 'workflow_dispatch'" && step.With["ref"] == "${{ github.sha }}" && step.With["path"] == ".release-tools" && !step.Allow
		}
	}
	if !currentTools {
		t.Fatal("recovery must load current publication tools separately from the tagged SDK source")
	}
	checkSDKStep(t, decision, "Select publication tools", "RELEASE_TOOLS=$GITHUB_WORKSPACE/.release-tools", "RELEASE_TOOLS=$GITHUB_WORKSPACE")
	checkSDKStep(t, decision, "Select TypeScript SDK release", `go -C "$RELEASE_TOOLS" run ./cmd/verify-release`, `node "$RELEASE_TOOLS/bin/typescript-release.mjs" "$GITHUB_WORKSPACE"`)
	checkSDKAction(t, workflow.Jobs["release"], "actions/download-artifact@", "name", "release-typescript-entry")
	checkSDKAction(t, workflow.Jobs["release"], "actions/download-artifact@", "path", "dist")
}
