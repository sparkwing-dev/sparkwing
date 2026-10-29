//go:build !windows

package source_test

import (
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

func TestSelectLeavesTheSharedTemporaryParentUnchanged(t *testing.T) {
	remote := "https://8.8.8.8/team/repo"
	sha := sourcetest.FixedRemote(t, remote)
	environment := sourcetest.Environment(t)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", parent)
	_, _, _, err := source.Select(t.Context(), source.Spec{RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote}, "build", runspec.Git{SHA: sha}, runspec.Source{}, "", source.Options{Access: &access{}, Base: procenv.Base{Local: true, Captured: environment}, BuildEnvironment: sourcetest.BuildEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("source selection changed shared temp permissions to %04o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("source selection left temporary directories: %v", entries)
	}
}
