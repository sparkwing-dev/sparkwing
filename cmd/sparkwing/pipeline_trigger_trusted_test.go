package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

const trustedController = "https://controller.example"

func trustedConfig(t *testing.T, runner string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(runner), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A trusted trigger pins this machine's repository-scoped runner only for a
// clean checkout whose HEAD is the pushed branch head; each other case is
// refused before submission, since that run could not count.
func TestTrustedRunnerRefusesWhatCannotCount(t *testing.T) {
	enrolled := "runner:\n  controller: " + trustedController + "/\n  holder_prefix: moonborn\n  repos: [github.com/acme/repo]\n"
	for name, tc := range map[string]struct {
		config  string
		prepare func(t *testing.T, work string)
		want    string
	}{
		"enrolled, clean and pushed": {config: enrolled},
		"no runner":                  {config: "run: {}\n", want: "no runner enrolled"},
		"runner on another controller": {
			config: strings.Replace(enrolled, trustedController, "https://other.example", 1), want: "no runner enrolled",
		},
		"runner without --repos": {
			config: "runner:\n  controller: " + trustedController + "\n  holder_prefix: laptop\n", want: "not scoped to repositories",
		},
		"dirty checkout": {
			config: enrolled, want: "uncommitted changes",
			prepare: func(t *testing.T, work string) {
				if err := os.WriteFile(filepath.Join(work, "edit.go"), []byte("package x\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		"unpushed commit": {
			config: enrolled, want: "not the head of origin/main",
			prepare: func(t *testing.T, work string) {
				gitInRepo(t, work, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgSign=false", "commit", "--quiet", "--allow-empty", "-m", "unpushed")
			},
		},
		"detached head": {
			config: enrolled, want: "not on a branch",
			prepare: func(t *testing.T, work string) { gitInRepo(t, work, "checkout", "--quiet", "--detach") },
		},
	} {
		t.Run(name, func(t *testing.T) {
			work, _, _ := pushedAndLocalCommits(t)
			gitInRepo(t, work, "push", "--quiet", "origin", "main")
			if tc.prepare != nil {
				tc.prepare(t, work)
			}
			runner, err := trustedRunner(t.Context(), trustedController, trustedConfig(t, tc.config), work)
			if tc.want == "" {
				if err != nil || runner != "moonborn" {
					t.Fatalf("runner = %q, %v; want moonborn", runner, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runner = %q, err = %v; want a refusal containing %q", runner, err, tc.want)
			}
		})
	}
}

func TestTrustedFlagRefusesAnotherPlacementOrRevision(t *testing.T) {
	for _, flags := range []triggerFlags{{trusted: true, runner: "cloud"}, {trusted: true, workingTree: true}, {trusted: true, ref: "main"}, {trusted: true, pipelineRef: "main"}} {
		if err := checkTrustedFlags(flags); err == nil {
			t.Errorf("checkTrustedFlags(%+v) accepted a conflicting flag", flags)
		}
	}
	if err := checkTrustedFlags(triggerFlags{trusted: true}); err != nil {
		t.Fatal(err)
	}
}

// The request a trusted trigger submits must seal proof eligible: the runner
// pin and profile are neutral, and any submitted variable would disqualify it,
// which is why a trusted trigger submits none.
func TestTrustedTriggerRequestIsProofEligible(t *testing.T) {
	repo, _ := triggerPreparationRepo(t)
	selection, err := prepareTriggerSource(t.Context(), triggerPreparationInput(repo))
	if err != nil {
		t.Fatal(err)
	}
	inputs := settings.Inputs{Context: settings.Context{NumCPU: 4}, Flags: settings.Values{Options: runspec.Options{Profile: runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: "prod"}}}}}
	inputs.Flags.Runner.Set, inputs.Flags.Runner.Value = true, runnerFlagSelection("moonborn")
	declarations := declarationFixture(t, []nodewire.PipelineDescription{{Name: "deploy-prod"}, {Name: "Deploy", Args: []nodewire.ArgumentDescription{{Name: "region", Type: "string"}}}})
	input := triggerRequestInput{home: t.TempDir(), source: selection, settings: inputs, args: []string{"--region", "west"}, declarations: declarations}
	request, err := prepareTriggerRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	if !request.Request.ProofEligible() {
		t.Fatalf("a trusted trigger request is not proof eligible: %+v", request.Request.Options)
	}
	input.submittedEnv = map[string][]byte{"GOFLAGS": []byte("-tags=x")}
	if request, err = prepareTriggerRequest(input); err != nil || request.Request.ProofEligible() {
		t.Fatalf("a request with a submitted variable stayed proof eligible: %v", err)
	}
}
