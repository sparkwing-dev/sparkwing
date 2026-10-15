package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestScaffoldGuidanceUsesAcceptedCommands(t *testing.T) {
	for _, shape := range []string{"minimal", "scheduled-report"} {
		t.Run(shape, func(t *testing.T) {
			t.Setenv("SPARKWING_HOME", t.TempDir())
			repo := t.TempDir()
			t.Chdir(repo)
			t.Setenv("PATH", t.TempDir())
			var commandErr error
			output := captureStdout(t, func() {
				commandErr = runSparkwing([]string{"pipeline", "new", "--pipeline", "sample", "--template", shape})
			})
			if commandErr != nil {
				t.Fatal(commandErr)
			}
			checkGuidanceCommands(t, output)
			for _, name := range []string{"README.md", "sparkwing.yaml"} {
				body, err := os.ReadFile(filepath.Join(repo, ".sparkwing", name))
				if err != nil {
					t.Fatal(err)
				}
				checkGuidanceCommands(t, string(body))
			}
		})
	}
}

func TestRepositoryUpgradeGuidanceUsesAcceptedCommand(t *testing.T) {
	checkGuidanceCommands(t, repoSuggestion(repoInfo{GuidesBehind: 2, Latest: "v1.2.3"}))
}

func checkGuidanceCommands(t *testing.T, text string) {
	t.Helper()
	commands := regexp.MustCompile("sparkwing [^`\n]+").FindAllString(text, -1)
	if len(commands) == 0 {
		t.Fatalf("guidance contains no commands: %q", text)
	}
	for _, command := range commands {
		command, _, _ = strings.Cut(command, "  ")
		command = strings.NewReplacer(
			"<name>", "sample", "<n>", "sample", "<example>", "sample",
			"[--template <shape>]", "--template minimal", "<shape>", "minimal",
			"<c>", "build", "<aws|gcp>", "aws", "<what you are doing>", "build",
		).Replace(command)
		words := regexp.MustCompile(`"[^"]*"|'[^']*'|[^\s]+`).FindAllString(command, -1)
		for i := range words {
			words[i] = strings.Trim(words[i], `"'`)
		}
		if _, err := clisurface.Parse("sparkwing", words[1:]); err != nil {
			t.Errorf("suggested command %q is rejected: %v", command, err)
		}
	}
}
