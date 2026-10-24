package controller

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if _, err := runnerflags.Parse(words[0], words[1:], io.Discard); err != nil {
		t.Fatal(err)
	}
}
