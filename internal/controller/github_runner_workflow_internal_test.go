package controller

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/runners/runnerflags"
)

func TestGitHubRunnerWorkflowDocMatchesTheRenderedTemplate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "github-actions-runners.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.ReplaceAll(string(raw), "\r\n", "\n")
	want := gitHubRunnerWorkflow("https://sparkwing.example.com", "acme")
	if !strings.Contains(doc, want) {
		t.Fatalf("docs/github-actions-runners.md does not carry the workflow the controller renders:\n%s", want)
	}
	if !strings.Contains(doc, GitHubActionsLabel) {
		t.Fatal("doc does not name the runner label")
	}
}

func TestGitHubRunnerWorkflowUsesCurrentGrammar(t *testing.T) {
	workflow := gitHubRunnerWorkflow("https://sparkwing.example.com", "acme")
	_, command, found := strings.Cut(workflow, `"$RUNNER_TEMP/sparkwing-runner" `)
	if !found {
		t.Fatal("workflow contains no runner invocation")
	}
	words := strings.Fields(strings.ReplaceAll(command, "\\\n", " "))
	if len(words) == 0 {
		t.Fatal("workflow contains no runner command")
	}
	if _, err := runnerflags.Parse(words[0], words[1:], io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestFallbackWorkflowIsDispatchOnlyAndBindsReceiptName(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "sparkwing-fallback.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		RunName string `yaml:"run-name"`
		On      map[string]struct {
			Inputs map[string]struct {
				Required bool `yaml:"required"`
			} `yaml:"inputs"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			RunsOn string `yaml:"runs-on"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	dispatch, ok := workflow.On["workflow_dispatch"]
	if !ok || len(workflow.On) != 1 || !dispatch.Inputs["receipt"].Required || !dispatch.Inputs["runner"].Required {
		t.Fatal("fallback workflow must require a receipt and image and start only on dispatch")
	}
	if workflow.RunName != "sparkwing:${{ inputs.receipt }}" || workflow.Jobs["sparkwing"].RunsOn != "${{ inputs.runner }}" {
		t.Fatal("fallback workflow lost provider receipt/image binding")
	}
	if workflow.Permissions["id-token"] != "write" || workflow.Permissions["contents"] != "read" || len(workflow.Permissions) != 2 {
		t.Fatal("fallback workflow changed its credential permissions")
	}
}
