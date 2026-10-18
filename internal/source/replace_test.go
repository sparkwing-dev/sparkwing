package source_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

func FuzzSealedCommitIgnoresReplacementObjects(f *testing.F) {
	f.Add("replacement")
	f.Add("other tree\n")
	f.Fuzz(func(t *testing.T, replacement string) {
		if len(replacement) > 4096 {
			t.Skip()
		}
		repo, original := sourcetest.Repository(t)
		sourcetest.Write(t, filepath.Join(repo, ".sparkwing", "pipeline.go"), replacement)
		sourcetest.Git(t, repo, "add", ".")
		sourcetest.Git(t, repo, "commit", "--allow-empty", "-qm", "replacement")
		other := sourcetest.Git(t, repo, "rev-parse", "HEAD")
		sourcetest.Git(t, repo, "replace", original, other)
		sourcetest.Git(t, repo, "config", "core.useReplaceRefs", "true")
		sourcetest.Git(t, repo, "config", "core.replaceRefsBase", "refs/replace/")
		options := source.Options{Workspace: t.TempDir(), Base: procenv.Base{Local: true, Captured: append(sourcetest.Environment(t), "GIT_NO_REPLACE_OBJECTS=0")}}
		entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "local:fixture", LocalCheckout: repo, SHA: original, CommittedCheckout: true}, options)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := entry.Close(); err != nil {
				t.Error(err)
			}
		}()
		body, err := os.ReadFile(filepath.Join(entry.Directory, "pipeline.go"))
		if err != nil || string(body) != "package main\n" || entry.SHA != original {
			t.Fatalf("sealed commit materialized substituted tree: %q %s %v", body, entry.SHA, err)
		}
		if refs := sourcetest.Git(t, entry.CheckoutDirectory(), "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "refs/replace/") {
			t.Fatal("operational refs entered acquired checkout")
		}
	})
}
