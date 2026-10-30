package main

import (
	"os"
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
	Needs    yaml.Node
	If       string
	Allow    bool `yaml:"continue-on-error"`
	Steps    []sdkReleaseStep
	Strategy struct {
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
	if job.If != "" || job.Allow || !hasSDKDependency(job, "validate-tag") {
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
	if assembly.If != "${{ !inputs.verify_only }}" || assembly.Allow || !hasSDKDependency(assembly, "typescript-native") || !hasSDKDependency(assembly, "validate-tag") {
		t.Fatal("package assembly must depend on successful native work and the resolved source")
	}
	checkSDKAction(t, assembly, "actions/checkout@", "ref", "${{ needs.validate-tag.outputs.source_sha }}")
	for _, arch := range []string{"x64", "arm64"} {
		checkSDKAction(t, assembly, "actions/download-artifact@", "path", "sdk/typescript/native/win32-"+arch)
	}
	checkSDKStep(t, assembly, "Assemble TypeScript package", "release:pack", "--version", "--destination")
	publication := workflow.Jobs["release"]
	for _, name := range []string{"dashboard", "typescript-package", "release"} {
		if workflow.Jobs[name].If != "${{ !inputs.verify_only }}" {
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
	checkSDKStep(t, publication, "Publish TypeScript package", "publish-typescript.mjs", "typescript-dist/", "${TAG#v}")
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
		if step.If != "" || step.Allow {
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
		if strings.HasPrefix(step.Uses, prefix) && step.With[key] == value && step.If == "" && !step.Allow {
			return
		}
	}
	t.Fatalf("missing required %s action with %s=%q", prefix, key, value)
}
