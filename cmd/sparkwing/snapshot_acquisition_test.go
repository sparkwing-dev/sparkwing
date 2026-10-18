package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

func TestSnapshotAcquisitionKeepsCapturedBytes(t *testing.T) {
	repo, _ := triggerPreparationRepo(t)
	writeSnapshotFile(t, repo, ".gitattributes", "*.txt text eol=crlf\n*.id ident\n", 0o644)
	runSnapshotGit(t, repo, "add", ".gitattributes")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "attributes")
	writeSnapshotFile(t, repo, "notes.txt", "first\nsecond\n", 0o644)
	writeSnapshotFile(t, repo, "version.id", "$Id$\n", 0o644)
	snapshot, err := captureWorktreeSnapshot(t.Context(), repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	replica, err := snapshotTriggerCheckout(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := triggerInspectionCheckout(t.Context(), replica, snapshot.SHA, snapshot.BundlePath, snapshot.tempDir)
	if err != nil {
		t.Fatal(err)
	}
	remote := acquireSnapshotBundle(t, snapshot, source.Spec{SHA: snapshot.SHA, PipelineDir: "."})
	for name, checkout := range map[string]string{"local replica": replica, "local inspection": inspection, "remote": remote.CheckoutDirectory()} {
		for _, file := range []string{"notes.txt", "version.id", ".gitattributes"} {
			want, err := os.ReadFile(filepath.Join(repo, file))
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(checkout, file))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("%s %s=%q, want captured %q (%v)", name, file, got, want, err)
			}
		}
	}
}

func TestSnapshotBundleCarriesEverySelectedRevision(t *testing.T) {
	repo, base := triggerPreparationRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	runSnapshotGit(t, repo, "clone", "--quiet", "--bare", repo, origin)
	commit := func(branch, name string) string {
		runSnapshotGit(t, repo, "checkout", "--quiet", "-b", branch, base)
		writeSnapshotFile(t, repo, name, name+"\n", 0o644)
		runSnapshotGit(t, repo, "add", name)
		runSnapshotGit(t, repo, "commit", "--quiet", "-m", name)
		return strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	}
	execution, code := commit("execution", "execution.txt"), commit("code", "code.txt")
	runSnapshotGit(t, repo, "checkout", "--quiet", "-b", "work", base)
	writeSnapshotFile(t, repo, "dirty.txt", "dirty\n", 0o644)
	snapshot, err := captureWorktreeSnapshot(t.Context(), repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	replica, err := snapshotTriggerCheckout(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	input := triggerPreparationInput(repo)
	input.checkout, input.ref, input.pipelineRef = replica, execution, code
	selected, err := prepareTriggerSource(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.includeRevisions(t.Context(), selected.git.SHA, selected.source.Ref); err != nil {
		t.Fatal(err)
	}
	sourcetest.Remote(t, map[string]string{"https://8.8.8.8/acme/app": origin})
	entry := acquireSnapshotBundle(t, snapshot, source.Spec{SHA: selected.git.SHA, PipelineSHA: selected.source.Ref, PipelineDir: ".sparkwing"})
	if entry.SHA != execution {
		t.Fatalf("execution SHA=%s, want %s", entry.SHA, execution)
	}
	for file, checkout := range map[string]string{"execution.txt": entry.CheckoutDirectory(), "code.txt": filepath.Dir(entry.Directory)} {
		if _, err := os.Stat(filepath.Join(checkout, file)); err != nil {
			t.Fatal(err)
		}
	}
}

func acquireSnapshotBundle(t *testing.T, snapshot *worktreeSnapshot, spec source.Spec) *source.Entry {
	t.Helper()
	bundle, err := os.ReadFile(snapshot.BundlePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bundle)
	spec.RepoKey, spec.RemoteURL, spec.BundleDigest = "git:8.8.8.8/acme/app", "https://8.8.8.8/acme/app", hex.EncodeToString(sum[:])
	entry, err := source.PrepareCheckout(t.Context(), spec, source.Options{Workspace: t.TempDir(), Access: snapshotRemoteAccess{}, BundleTransport: burstBundleTransport{bundle}, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	})
	return entry
}

type snapshotRemoteAccess struct{ burstBundleAccess }

func (snapshotRemoteAccess) GitCredential(context.Context, source.Spec) (source.Credential, error) {
	return source.Credential{Kind: "https", Host: "8.8.8.8", Username: "source", Secret: "private"}, nil
}
