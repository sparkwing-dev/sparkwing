package source_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func TestPreparedCheckoutUsesInitSourceWithoutAnotherCredential(t *testing.T) {
	dir, sha := sourcetest.Repository(t)
	sourcetest.Git(t, dir, "remote", "add", "origin", "https://github.com/acme/project.git")
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "gh:42", RemoteURL: "https://github.com/acme/project", SHA: sha}, source.Options{PreparedCheckout: dir, Base: procenv.Base{Ambient: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	if entry.SHA != sha || entry.RepoKey != "gh:42" || entry.DefaultBranch != "trunk" || entry.Directory != filepath.Join(dir, ".sparkwing") {
		t.Fatalf("entry=%+v", entry)
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
}

func FuzzPreparedCheckoutCannotSubstituteSource(f *testing.F) {
	f.Add("https://github.com/other/project", uint8(0))
	f.Add("https://github.com/acme/project", uint8(1))
	f.Add("https://github.com/acme/project", uint8(2))
	f.Add("https://github.com/acme/project", uint8(3))
	f.Add("https://github.com/acme/project", uint8(4))
	f.Fuzz(func(t *testing.T, remote string, change uint8) {
		dir, sha := sourcetest.Repository(t)
		sourcetest.Git(t, dir, "remote", "add", "origin", "https://github.com/acme/project")
		spec := source.Spec{RepoKey: "gh:42", RemoteURL: remote, SHA: sha}
		if change%5 == 1 {
			spec.SHA = strings.Repeat("f", 40)
		}
		if change%5 == 2 {
			sourcetest.Write(t, filepath.Join(dir, ".sparkwing", "pipeline.go"), "package main // changed\n")
		}
		if change%5 == 3 || change%5 == 4 {
			if change%5 == 4 {
				sourcetest.Write(t, filepath.Join(dir, ".git", "info", "exclude"), ".sparkwing/injected.go\n")
			}
			sourcetest.Write(t, filepath.Join(dir, ".sparkwing", "injected.go"), "package main\n")
		}
		entry, err := source.PrepareCheckout(t.Context(), spec, source.Options{PreparedCheckout: dir, Base: procenv.Base{Ambient: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
		if entry != nil {
			_ = entry.Close()
		}
		identity, identityErr := sourceurl.CaseIdentity(remote)
		if identityErr != nil || identity != "github.com/acme/project" || change%5 != 0 {
			if err == nil {
				t.Fatal("prepared checkout substituted remote, commit or source bytes")
			}
		}
	})
}
