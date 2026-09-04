package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func goWorkFixture(t *testing.T, use string) (moduleDir string) {
	t.Helper()
	root := t.TempDir()
	moduleDir = filepath.Join(root, "worktree")
	if err := os.MkdirAll(moduleDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.com/repo\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "go.work")
	if err := os.WriteFile(work, []byte("go 1.26\n\nuse "+use+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOWORK", work)
	return moduleDir
}

func TestGoWorkNoteNamesTheCommandThatWorks(t *testing.T) {
	moduleDir := goWorkFixture(t, "./checkout")

	note := goWorkNote(moduleDir)
	for _, want := range []string{
		"GOWORK=off go -C " + moduleDir,
		"does not contain modules listed in go.work",
		"-C` on its own still fails",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note omits %q: %s", want, note)
		}
	}
}

func TestGoWorkNoteReportsFromASubdirectoryOfTheModule(t *testing.T) {
	moduleDir := goWorkFixture(t, "./checkout")
	sub := filepath.Join(moduleDir, "cmd", "tool")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}

	if note := goWorkNote(sub); !strings.Contains(note, "go -C "+moduleDir+" build") {
		t.Errorf("note must name the module root, not the working directory: %s", note)
	}
}

func TestGoWorkNoteIsSilentWhenTheWorkspaceListsTheModule(t *testing.T) {
	moduleDir := goWorkFixture(t, "./worktree")

	if note := goWorkNote(moduleDir); note != "" {
		t.Errorf("workspace covers the module; want no note, got: %s", note)
	}
}

func TestGoWorkNoteIsSilentWhenTheWorkspaceIsAlreadyOff(t *testing.T) {
	moduleDir := goWorkFixture(t, "./checkout")
	t.Setenv("GOWORK", "off")

	if note := goWorkNote(moduleDir); note != "" {
		t.Errorf("GOWORK=off already resolves it; want no note, got: %s", note)
	}
}

func TestGoWorkNoteIsSilentOutsideAnyModule(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOWORK", "")

	if note := goWorkNote(root); note != "" {
		t.Errorf("no go.mod above %s; want no note, got: %s", root, note)
	}
}
