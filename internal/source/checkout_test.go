package source_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestPrepareCheckoutLocalPreservesWorkingTreeAndOriginHead(t *testing.T) {
	dir, sha := sourcetest.Repository(t)
	sourcetest.Write(t, filepath.Join(dir, ".sparkwing", "pipeline.go"), "package main // working tree\n")
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir}, source.Options{Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
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
	entry, err := source.PrepareCheckout(t.Context(), spec, source.Options{Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
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
	if _, err := source.PrepareCheckout(t.Context(), spec, source.Options{Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment}); err == nil {
		t.Fatal("accepted a different local HEAD")
	}
}

func TestPrepareCheckoutUnresolvedDefaultStaysAbsent(t *testing.T) {
	dir, _ := sourcetest.Repository(t)
	sourcetest.Git(t, dir, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir}, source.Options{Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = entry.Close() })
	if entry.DefaultBranch != "" {
		t.Fatalf("invented default branch %q", entry.DefaultBranch)
	}
}

func TestPrepareCheckoutLocalBeforeFirstCommit(t *testing.T) {
	dir := t.TempDir()
	sourcetest.Git(t, dir, "init", "--quiet")
	if err := os.Mkdir(filepath.Join(dir, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	sourcetest.Write(t, filepath.Join(dir, ".sparkwing", "pipeline.go"), "package main\n")
	options := source.Options{Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment}
	spec := source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir}
	entry, err := source.PrepareCheckout(t.Context(), spec, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
	if entry.SHA != "" || entry.Directory != filepath.Join(dir, ".sparkwing") {
		t.Fatalf("uncommitted checkout facts = %+v", entry)
	}
	spec.SHA = strings.Repeat("a", 40)
	if entry, err := source.PrepareCheckout(t.Context(), spec, options); err == nil {
		_ = entry.Close()
		t.Fatal("checkout without a commit satisfied a sealed SHA")
	}
}

func TestPrepareCheckoutLocalRefusesBrokenHEAD(t *testing.T) {
	for _, head := range []string{strings.Repeat("a", 40) + "\n", "ref: refs/heads/missing-object\n"} {
		t.Run(head, func(t *testing.T) {
			dir := t.TempDir()
			sourcetest.Git(t, dir, "init", "--quiet")
			if err := os.Mkdir(filepath.Join(dir, ".sparkwing"), 0o700); err != nil {
				t.Fatal(err)
			}
			sourcetest.Write(t, filepath.Join(dir, ".git", "HEAD"), head)
			if strings.HasPrefix(head, "ref:") {
				sourcetest.Write(t, filepath.Join(dir, ".git", "refs", "heads", "missing-object"), strings.Repeat("a", 40)+"\n")
			}
			entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir}, source.Options{Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
			if err == nil {
				_ = entry.Close()
				t.Fatal("broken HEAD was treated as an unborn checkout")
			}
		})
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
			_, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir, PipelineDir: path}, source.Options{Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
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
	read       func(context.Context, source.Spec) (source.Credential, error)
}

func (a *access) GitCredential(ctx context.Context, spec source.Spec) (source.Credential, error) {
	a.calls++
	if a.read != nil {
		return a.read(ctx, spec)
	}
	return a.credential, a.err
}

func (a *access) BundleURL(context.Context, string) (string, error) {
	return "", errors.New("unexpected bundle request")
}

func TestPrepareCheckoutCancellationStopsCredentialRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	reached := make(chan struct{})
	a := &access{read: func(ctx context.Context, _ source.Spec) (source.Credential, error) {
		close(reached)
		<-ctx.Done()
		return source.Credential{}, context.Cause(ctx)
	}}
	workspace := t.TempDir()
	options := source.Options{Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment, Workspace: workspace, Access: a}
	var returned *source.Entry
	done := make(chan error, 1)
	go func() {
		entry, err := source.PrepareCheckout(ctx, source.Spec{
			RepoKey: "git:github.com/team/repo", RemoteURL: "https://github.com/team/repo", SHA: strings.Repeat("a", 40),
		}, options)
		returned = entry
		if entry != nil {
			err = errors.Join(err, errors.New("cancelled checkout returned an entry"), entry.Close())
		}
		done <- err
	}()
	select {
	case <-reached:
	case <-ctx.Done():
		t.Fatal("source acquisition did not reach credential release")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || returned != nil {
			t.Fatalf("source cancellation returned %v, %v", returned, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("source acquisition did not join credential release")
	}
	remaining, err := os.ReadDir(workspace)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("cancelled acquisition left a workspace: %v, %v", remaining, err)
	}
}

func TestPrepareCheckoutRequiresSealedSHAAndAuthorizedCredential(t *testing.T) {
	for _, sha := range []string{"", "main", "--upload-pack=evil", strings.Repeat("z", 40)} {
		_, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:github.com/team/repo", RemoteURL: "https://github.com/team/repo", SHA: sha}, source.Options{Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment, Workspace: t.TempDir()})
		if err == nil {
			t.Fatalf("accepted noncommit %q", sha)
		}
	}
	spec := source.Spec{RepoKey: "git:github.com/team/repo", RemoteURL: "https://github.com/team/repo", SHA: strings.Repeat("a", 40)}
	denied := errors.New("source release denied")
	a := &access{err: denied}
	_, err := source.PrepareCheckout(t.Context(), spec, source.Options{Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment, Workspace: t.TempDir(), Access: a})
	if !errors.Is(err, denied) || a.calls != 1 {
		t.Fatalf("release denial = %v; calls = %d", err, a.calls)
	}
	a = &access{credential: source.Credential{Kind: "https", Host: "other.example", Username: "owner", Secret: "private-password"}}
	_, err = source.PrepareCheckout(t.Context(), spec, source.Options{Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment, Workspace: t.TempDir(), Access: a})
	if err == nil || strings.Contains(err.Error(), a.credential.Secret) {
		t.Fatalf("credential host refusal = %v", err)
	}
}
