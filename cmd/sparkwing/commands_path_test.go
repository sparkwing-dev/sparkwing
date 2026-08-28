package main

import (
	"strings"
	"testing"
)

func TestMatchesCommandPathAcceptsBothSpellingsOfAPrefix(t *testing.T) {
	cases := []struct {
		path   string
		prefix string
		want   bool
	}{
		{"sparkwing runs", "runs", true},
		{"sparkwing runs list", "runs", true},
		{"sparkwing runs list", "runs list", true},
		{"sparkwing runs", "sparkwing runs", true},
		{"sparkwing runs list", "sparkwing runs", true},
		{"sparkwing runs", "sparkwing", true},
		{"sparkwing runs", "", true},
		{"sparkwing run", "runs", false},
		{"sparkwing pipeline", "runs", false},
		{"sparkwing pipeline", "sparkwing runs", false},
		{"sparkwing run", "run", true},
		{"sparkwing run config", "run", true},
		{"sparkwing runs", "run", false},
		{"sparkwing runs list", "run", false},
		{"sparkwing runs", "sparkwing run", false},
		{"sparkwing runs list", "runs lis", false},
		{"sparkwing pipeline", "pipe", false},
	}
	for _, tc := range cases {
		if got := matchesCommandPath(tc.path, tc.prefix); got != tc.want {
			t.Errorf("matchesCommandPath(%q, %q) = %v, want %v", tc.path, tc.prefix, got, tc.want)
		}
	}
}

func TestCommandsPathSelectsTheSameSubtreeEitherWay(t *testing.T) {
	bare := commandsOutput(t, "--path", "runs", "-o", "plain")
	qualified := commandsOutput(t, "--path", "sparkwing runs", "-o", "plain")
	if bare != qualified {
		t.Fatalf("--path runs and --path \"sparkwing runs\" disagree:\nbare:\n%s\nqualified:\n%s", bare, qualified)
	}
	if !strings.Contains(bare, "sparkwing runs list") {
		t.Fatalf("--path runs did not select the runs subtree:\n%s", bare)
	}
}

func TestCommandsRefusesAPathThatMatchesNothing(t *testing.T) {
	for _, output := range []string{"pretty", "json", "plain", "markdown"} {
		err := runCommandsQuiet(t, "--path", "nosuchsubtree", "-o", output)
		if err == nil {
			t.Fatalf("-o %s: unmatched --path returned no error", output)
		}
		if !strings.Contains(err.Error(), "nosuchsubtree") {
			t.Fatalf("-o %s: error %q does not name the path", output, err)
		}
	}
}

func TestCommandsPathMatchesWholeComponentsOnly(t *testing.T) {
	selected := strings.Split(strings.TrimSpace(commandsOutput(t, "--path", "run", "-o", "plain")), "\n")
	if len(selected) == 0 || selected[0] == "" {
		t.Fatal("--path run selected nothing")
	}
	for _, path := range selected {
		if strings.HasPrefix(path, "sparkwing runs") {
			t.Fatalf("--path run selected %q from the separate runs group", path)
		}
	}
	runs := commandsOutput(t, "--path", "runs", "-o", "plain")
	if !strings.Contains(runs, "sparkwing runs list") {
		t.Fatalf("--path runs stopped selecting its own subtree:\n%s", runs)
	}
}

func TestCommandsRefusesAWhitespacePath(t *testing.T) {
	err := runCommandsQuiet(t, "--path", "   ", "-o", "plain")
	if err == nil {
		t.Fatal("a whitespace --path returned no error")
	}
	if !strings.Contains(err.Error(), "matched no command") {
		t.Fatalf("error %q does not report an unmatched filter", err)
	}
}

func TestCommandsPointsAtIncludeHiddenWhenOnlyHiddenMatched(t *testing.T) {
	hidden := ""
	for _, c := range allCommands {
		if c.Hidden {
			hidden = c.Path
			break
		}
	}
	if hidden == "" {
		t.Skip("no hidden command in the registry")
	}
	err := runCommandsQuiet(t, "--path", hidden, "-o", "plain")
	if err == nil || !strings.Contains(err.Error(), "--include-hidden") {
		t.Fatalf("--path %q (hidden) = %v, want an error naming --include-hidden", hidden, err)
	}
	if err := runCommandsQuiet(t, "--path", hidden, "--include-hidden", "-o", "plain"); err != nil {
		t.Fatalf("--path %q --include-hidden = %v, want it listed", hidden, err)
	}
}

func commandsOutput(t *testing.T, args ...string) string {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runCommands(args) })
	if err != nil {
		t.Fatalf("commands %v: %v", args, err)
	}
	return out
}

func runCommandsQuiet(t *testing.T, args ...string) error {
	t.Helper()
	var err error
	captureStdout(t, func() { err = runCommands(args) })
	return err
}
