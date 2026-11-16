package opsview

import (
	"slices"
	"testing"
)

func TestDisplayLinesBreaksOnNewlinesAndQuotesOtherControls(t *testing.T) {
	got := DisplayLines("command failed (exit 1)\r\nFAIL: \x1b[31mred\nok\n")
	want := []string{"command failed (exit 1)", `"FAIL: \x1b[31mred"`, "ok"}
	if !slices.Equal(got, want) {
		t.Fatalf("DisplayLines = %q, want %q", got, want)
	}
}

func TestDisplayLinesLimitsEachLine(t *testing.T) {
	got := DisplayLines("abcdefgh\nxy", 5)
	want := []string{"ab...", "xy"}
	if !slices.Equal(got, want) {
		t.Fatalf("DisplayLines = %q, want %q", got, want)
	}
}

func TestDisplayBlockIndentsContinuationLines(t *testing.T) {
	if got, want := DisplayBlock("first\nsecond\nthird", "  "), "first\n  second\n  third"; got != want {
		t.Fatalf("DisplayBlock = %q, want %q", got, want)
	}
	if got := DisplayBlock("one line", "  "); got != "one line" {
		t.Fatalf("single line changed: %q", got)
	}
}
