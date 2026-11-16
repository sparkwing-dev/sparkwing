package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The proof creates OS users and system services, so it must refuse every
// host that is not a discarded GitHub-hosted VM before it writes anything.
func TestRunnerIsolationProofRefusesOutsideDisposableRunner(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash unavailable: %v", err)
	}
	cases := map[string][]string{
		"no Actions":   {},
		"self-hosted":  {"GITHUB_ACTIONS=true", "RUNNER_ENVIRONMENT=self-hosted", "SPARKWING_ISOLATION_PROOF_VM=disposable"},
		"not declared": {"GITHUB_ACTIONS=true", "RUNNER_ENVIRONMENT=github-hosted"},
		"wrong marker": {"GITHUB_ACTIONS=true", "RUNNER_ENVIRONMENT=github-hosted", "SPARKWING_ISOLATION_PROOF_VM=yes"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			evidence := filepath.Join(dir, "evidence")
			cmd := exec.Command("bash", "runner-isolation-proof.sh", evidence)
			cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "RUNNER_TEMP=" + dir}, env...)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("exit = %v, want 2\n%s", err, output)
			}
			if !strings.Contains(string(output), "RESULT: REFUSED") {
				t.Fatalf("refusal not reported:\n%s", output)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("refusal wrote %d entries under RUNNER_TEMP", len(entries))
			}
		})
	}
}

func TestRunnerIsolationProofWorkflowIsManualReadOnlyAndSecretFree(t *testing.T) {
	body, err := os.ReadFile("../.github/workflows/runner-isolation-proof.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secrets.") {
		t.Fatal("the proof workflow must not read secrets")
	}
	var doc struct {
		On          map[string]any    `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Env         map[string]string `yaml:"env"`
			RunsOn      string            `yaml:"runs-on"`
			Steps       []struct {
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	triggers := make([]string, 0, len(doc.On))
	for trigger := range doc.On {
		triggers = append(triggers, trigger)
	}
	if !slices.Equal(triggers, []string{"workflow_dispatch"}) {
		t.Fatalf("triggers = %v, want workflow_dispatch only", triggers)
	}
	readOnly := map[string]string{"contents": "read"}
	if len(doc.Permissions) != 1 || doc.Permissions["contents"] != "read" {
		t.Fatalf("workflow permissions = %v, want %v", doc.Permissions, readOnly)
	}
	if len(doc.Jobs) != 1 {
		t.Fatalf("jobs = %d, want one", len(doc.Jobs))
	}
	for name, job := range doc.Jobs {
		if len(job.Permissions) != 1 || job.Permissions["contents"] != "read" {
			t.Fatalf("%s permissions = %v, want %v", name, job.Permissions, readOnly)
		}
		if !strings.HasPrefix(job.RunsOn, "ubuntu-") {
			t.Fatalf("%s runs on %q; the proof needs a disposable GitHub-hosted Ubuntu VM", name, job.RunsOn)
		}
		if job.Env["SPARKWING_ISOLATION_PROOF_VM"] != "disposable" {
			t.Fatalf("%s does not declare the disposable VM the script requires", name)
		}
		runs := false
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["persist-credentials"] != "false" {
				t.Fatalf("%s checkout leaves the job token on disk", name)
			}
			runs = runs || strings.Contains(step.Run, "bin/runner-isolation-proof.sh")
		}
		if !runs {
			t.Fatalf("%s never runs bin/runner-isolation-proof.sh", name)
		}
	}
}
