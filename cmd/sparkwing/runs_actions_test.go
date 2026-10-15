package main

import (
	"strings"
	"testing"
)

func TestCollectRunIDs_FlagsAndStdinDash(t *testing.T) {
	stdin := strings.NewReader("run-stdin-1\nrun-stdin-2\n")
	got, err := collectRunIDs(
		[]string{"run-flag-a", "-", "run-flag-b"},
		stdin,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run-flag-a", "run-flag-b", "run-stdin-1", "run-stdin-2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestCollectRunIDs_NoDashMeansNoStdinRead(t *testing.T) {
	stdin := strings.NewReader("should-not-be-read\n")
	got, err := collectRunIDs([]string{"run-a"}, stdin)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "run-a" {
		t.Errorf("got %v, want [run-a]", got)
	}
}

func TestCollectRunIDs_Dedup(t *testing.T) {
	stdin := strings.NewReader("run-a\nrun-b\n")
	got, err := collectRunIDs([]string{"run-a", "-"}, stdin)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("dedup failed: %v", got)
	}
}

func TestCollectRunIDs_SkipsBlankStdinLines(t *testing.T) {
	stdin := strings.NewReader("run-a\n\n   \nrun-b\n")
	got, err := collectRunIDs([]string{"-"}, stdin)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 entries", got)
	}
}

func TestCollectRunIDs_EmptyReturnsEmpty(t *testing.T) {
	got, err := collectRunIDs(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}
