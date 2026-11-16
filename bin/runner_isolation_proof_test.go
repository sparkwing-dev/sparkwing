package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The proof creates OS users and system services, so it must refuse every
// host that is not a discarded GitHub-hosted VM before it writes anything.
// Full runs are exercised only where an Actions variable is missing, since
// this suite itself runs on hosted VMs; the cases with every variable set use
// --preflight, which checks the same facts and never writes.
func TestRunnerIsolationProofRefusesOutsideDisposableRunner(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash unavailable: %v", err)
	}
	hosted := []string{"GITHUB_ACTIONS=true", "RUNNER_ENVIRONMENT=github-hosted"}
	ownedRecord := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ownedRecord, "imagegeneration"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownedRecord, "imagegeneration", "imagedata.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	type refusal struct {
		name    string
		env     []string
		args    []string
		refuses []string
	}
	cases := []refusal{
		{name: "run without Actions", args: []string{"EVIDENCE"}, refuses: []string{"GITHUB_ACTIONS"}},
		{name: "run on a self-hosted runner", env: []string{"GITHUB_ACTIONS=true", "RUNNER_ENVIRONMENT=self-hosted"}, args: []string{"EVIDENCE"}, refuses: []string{"RUNNER_ENVIRONMENT"}},
		{name: "every variable set, no host facts", env: hosted, args: []string{"--preflight", t.TempDir()}, refuses: []string{"imagedata.json", "Runner.Worker"}},
		{name: "every variable set, image record not root-owned", env: hosted, args: []string{"--preflight", ownedRecord}, refuses: []string{"imagedata.json", "Runner.Worker"}},
	}
	if _, err := os.Stat("/imagegeneration/imagedata.json"); errors.Is(err, os.ErrNotExist) {
		cases = append(cases, refusal{name: "every variable set on this host", env: hosted, args: []string{"--preflight"}, refuses: []string{"imagedata.json", "Runner.Worker"}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if os.Getuid() == 0 && tc.name == "every variable set, image record not root-owned" {
				t.Skip("as root the planted record is root-owned")
			}
			dir := t.TempDir()
			args := []string{"runner-isolation-proof.sh"}
			for _, arg := range tc.args {
				if arg == "EVIDENCE" {
					arg = filepath.Join(dir, "evidence")
				}
				args = append(args, arg)
			}
			cmd := exec.Command("bash", args...)
			cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "RUNNER_TEMP=" + dir}, tc.env...)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("exit = %v, want 2\n%s", err, output)
			}
			if !strings.Contains(string(output), "RESULT: REFUSED") {
				t.Fatalf("refusal not reported:\n%s", output)
			}
			for _, reason := range tc.refuses {
				if !strings.Contains(string(output), "refused: ") || !strings.Contains(string(output), reason) {
					t.Fatalf("refusal does not name %s:\n%s", reason, output)
				}
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
		if !strings.HasPrefix(job.RunsOn, "ubuntu-") || strings.Contains(job.RunsOn, "self-hosted") {
			t.Fatalf("%s runs on %q; the proof needs a disposable GitHub-hosted Ubuntu VM", name, job.RunsOn)
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

// Backgrounding a shell function makes $! a subshell, so killing that PID left
// the hosted proof's first controller serving its port. Every command the
// script backgrounds must therefore start with a program, never a function.
func TestRunnerIsolationProofBackgroundsNoShellFunction(t *testing.T) {
	body, err := os.ReadFile("runner-isolation-proof.sh")
	if err != nil {
		t.Fatal(err)
	}
	functionName := regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\(\)\s*\{`)
	functions := map[string]bool{}
	var commands []string
	var pending strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		if m := functionName.FindStringSubmatch(line); m != nil {
			functions[m[1]] = true
		}
		if strings.HasSuffix(strings.TrimRight(line, " \t"), `\`) {
			pending.WriteString(strings.TrimSuffix(strings.TrimRight(line, " \t"), `\`) + " ")
			continue
		}
		pending.WriteString(line)
		commands = append(commands, pending.String())
		pending.Reset()
	}
	backgrounded := 0
	for _, command := range commands {
		trimmed := strings.TrimSpace(command)
		if strings.HasPrefix(trimmed, "#") || !strings.HasSuffix(trimmed, "&") || strings.HasSuffix(trimmed, "&&") {
			continue
		}
		backgrounded++
		fields := strings.Fields(trimmed)
		if functions[fields[0]] {
			t.Errorf("backgrounds shell function %s, so $! is a subshell rather than the program: %s", fields[0], trimmed)
		}
	}
	if backgrounded == 0 {
		t.Fatal("found no backgrounded command; the scan matched nothing")
	}
	if !functions["start_controller"] {
		t.Fatal("found no shell functions; the scan matched nothing")
	}
}
