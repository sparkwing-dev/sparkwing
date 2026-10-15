package source_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"

	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestPrepareCheckoutLocalPreservesWorkingTreeAndOriginHead(t *testing.T) {
	dir, sha := sourcetest.Repository(t)
	sourcetest.Write(t, filepath.Join(dir, ".sparkwing", "pipeline.go"), "package main // working tree\n")
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir}, source.Options{GitEnvironment: sourcetest.Environment(t)})
	if err != nil {
		t.Fatal(err)
	}
	if entry.SHA != sha || entry.DefaultBranch != "trunk" || entry.Directory != filepath.Join(dir, ".sparkwing") {
		t.Fatalf("checkout facts = %+v", entry)
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(entry.Directory, "pipeline.go"))
	if err != nil || !strings.Contains(string(body), "working tree") {
		t.Fatalf("user checkout changed: %q, %v", body, err)
	}
}

func TestPrepareCheckoutKeepsSealedDefaultAndRefusesMovedLocalHEAD(t *testing.T) {
	dir, sha := sourcetest.Repository(t)
	spec := source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir, SHA: sha, DefaultBranch: "release"}
	entry, err := source.PrepareCheckout(t.Context(), spec, source.Options{GitEnvironment: sourcetest.Environment(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = entry.Close() })
	if entry.DefaultBranch != "release" {
		t.Fatalf("sealed default changed to %q", entry.DefaultBranch)
	}
	sourcetest.Write(t, filepath.Join(dir, "new"), "later")
	sourcetest.Git(t, dir, "add", "new")
	sourcetest.Git(t, dir, "commit", "-qm", "later")
	if _, err := source.PrepareCheckout(t.Context(), spec, source.Options{GitEnvironment: sourcetest.Environment(t)}); err == nil {
		t.Fatal("accepted a different local HEAD")
	}
}

func TestPrepareCheckoutUnresolvedDefaultStaysAbsent(t *testing.T) {
	dir, _ := sourcetest.Repository(t)
	sourcetest.Git(t, dir, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir}, source.Options{GitEnvironment: sourcetest.Environment(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = entry.Close() })
	if entry.DefaultBranch != "" {
		t.Fatalf("invented default branch %q", entry.DefaultBranch)
	}
}

func TestPrepareCheckoutRefusesEscapingEntrypoint(t *testing.T) {
	dir, _ := sourcetest.Repository(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../", outside, "escape"} {
		t.Run(path, func(t *testing.T) {
			_, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir, PipelineDir: path}, source.Options{GitEnvironment: sourcetest.Environment(t)})
			if err == nil {
				t.Fatal("accepted entry outside checkout")
			}
		})
	}
}

type access struct {
	credential source.Credential
	err        error
	calls      int
}

func (a *access) GitCredential(context.Context, source.Spec) (source.Credential, error) {
	a.calls++
	return a.credential, a.err
}

func (a *access) BundleURL(context.Context, string) (string, error) {
	return "", errors.New("unexpected bundle request")
}

func TestPrepareCheckoutRequiresSealedSHAAndAuthorizedCredential(t *testing.T) {
	for _, sha := range []string{"", "main", "--upload-pack=evil", strings.Repeat("z", 40)} {
		_, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:github.com/team/repo", RemoteURL: "https://github.com/team/repo", SHA: sha}, source.Options{GitEnvironment: sourcetest.Environment(t), Workspace: t.TempDir()})
		if err == nil {
			t.Fatalf("accepted noncommit %q", sha)
		}
	}
	spec := source.Spec{RepoKey: "git:github.com/team/repo", RemoteURL: "https://github.com/team/repo", SHA: strings.Repeat("a", 40)}
	denied := errors.New("source release denied")
	a := &access{err: denied}
	_, err := source.PrepareCheckout(t.Context(), spec, source.Options{GitEnvironment: sourcetest.Environment(t), Workspace: t.TempDir(), Access: a})
	if !errors.Is(err, denied) || a.calls != 1 {
		t.Fatalf("release denial = %v; calls = %d", err, a.calls)
	}
	a = &access{credential: source.Credential{Kind: "https", Host: "other.example", Username: "owner", Secret: "private-password"}}
	_, err = source.PrepareCheckout(t.Context(), spec, source.Options{GitEnvironment: sourcetest.Environment(t), Workspace: t.TempDir(), Access: a})
	if err == nil || strings.Contains(err.Error(), a.credential.Secret) {
		t.Fatalf("credential host refusal = %v", err)
	}
}
