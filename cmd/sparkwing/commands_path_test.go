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
	}
	for _, tc := range cases {
		if got := matchesCommandPath(tc.path, tc.prefix); got != tc.want {
			t.Errorf("matchesCommandPath(%q, %q) = %v, want %v", tc.path, tc.prefix, got, tc.want)
		}
	}
}

// TestCommandsPathSelectsTheSameSubtreeEitherWay pins the fix against the
// live registry rather than a fixture: the bare prefix has to reach the
// real commands, not just satisfy a string helper.
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

// TestCommandsRefusesAPathThatMatchesNothing keeps a mistyped filter from
// reading as an answer about the CLI. It used to exit 0 printing nothing,
// or the literal `null` under -o json.
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

// TestCommandsPointsAtIncludeHiddenWhenOnlyHiddenMatched separates "you
// misspelled it" from "it exists but is hidden", which are different
// mistakes with different fixes.
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

// commandsOutput runs the verb and returns what it printed.
func commandsOutput(t *testing.T, args ...string) string {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runCommands(args) })
	if err != nil {
		t.Fatalf("commands %v: %v", args, err)
	}
	return out
}

// runCommandsQuiet runs the verb for its error, discarding its output.
func runCommandsQuiet(t *testing.T, args ...string) error {
	t.Helper()
	var err error
	captureStdout(t, func() { err = runCommands(args) })
	return err
}
