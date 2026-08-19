package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestZshCompletionDoesNotRequestRemovedRunnerSelection(t *testing.T) {
	script := renderZsh()
	for _, stale := range []string{"--default-runner", "_complete-runners", "default_runner"} {
		if strings.Contains(script, stale) {
			t.Errorf("zsh completion contains removed runner selection %q", stale)
		}
	}
}

func TestRemovedRunnerCompletionHelperIsSilent(t *testing.T) {
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = original })

	if err := runInternalCompleteRunners(nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("removed runner completion emitted %q", got)
	}
}
