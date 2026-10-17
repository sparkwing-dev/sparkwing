package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func TestBurstSourceBundleTrackedEditsAndDeclaredFiles(t *testing.T) {
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "tracked.txt", "base\n", 0o644)
	writeSnapshotFile(t, repo, "removed.txt", "remove me\n", 0o644)
	writeSnapshotFile(t, repo, "secret.conf", "old private bytes\n", 0o600)
	writeSnapshotFile(t, repo, ".gitignore", "ignored.txt\n", 0o644)
	writeSnapshotFile(t, repo, ".gitattributes", "untracked.txt filter=unavailable\n", 0o644)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-m", "base")
	writeSnapshotFile(t, repo, "tracked.txt", "working\r\n", 0o644)
	writeSnapshotFile(t, repo, "secret.conf", "private local value\n", 0o600)
	writeSnapshotFile(t, repo, "declared.txt", "declared\n", 0o644)
	writeSnapshotFile(t, repo, "ignored.txt", "explicit ignored input\n", 0o644)
	writeSnapshotFile(t, repo, "untracked.txt", "exclude me\n", 0o644)
	writeSnapshotFile(t, repo, ".env", "API_TOKEN=exclude-me\n", 0o600)
	writeSnapshotFile(t, repo, "staged.txt", "staged\n", 0o644)
	runSnapshotGit(t, repo, "add", "staged.txt")
	if err := os.Remove(filepath.Join(repo, "removed.txt")); err != nil {
		t.Fatal(err)
	}
	before := runSnapshotGit(t, repo, "status", "--porcelain")
	snapshot, err := captureBurstSnapshot(t.Context(), repo, []string{"declared.txt", "ignored.txt", "secret.conf"}, []string{"secret.conf"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	checkout := importSnapshotBundle(t, snapshot)
	assertSnapshotFile(t, checkout, "tracked.txt", "working\r\n")
	assertSnapshotFile(t, checkout, "declared.txt", "declared\n")
	assertSnapshotFile(t, checkout, "ignored.txt", "explicit ignored input\n")
	assertSnapshotFile(t, checkout, "staged.txt", "staged\n")
	for _, name := range []string{"secret.conf", "removed.txt", "untracked.txt", ".env"} {
		if _, err := os.Lstat(filepath.Join(checkout, name)); !os.IsNotExist(err) {
			t.Fatalf("excluded path %q survived: %v", name, err)
		}
	}
	if after := runSnapshotGit(t, repo, "status", "--porcelain"); after != before {
		t.Fatalf("source index or working tree changed: %q -> %q", before, after)
	}
	for _, content := range []string{"old private bytes\n", "private local value\n", "exclude me\n"} {
		sha := strings.TrimSpace(runSnapshotGitInput(t, repo, content, "hash-object", "--stdin"))
		if snapshotObjectExists(t, checkout, sha) {
			t.Fatal("excluded content entered the bundle object database")
		}
	}
	bundle, err := os.ReadFile(snapshot.BundlePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bundle)
	digest := hex.EncodeToString(sum[:])
	acquired, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "example.test/team/repo", RemoteURL: "https://example.test/team/repo.git", SHA: snapshot.SHA, PipelineDir: ".", BundleDigest: digest}, source.Options{Workspace: t.TempDir(), Access: burstBundleAccess{}, BundleTransport: burstBundleTransport{bundle}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := acquired.Close(); err != nil {
			t.Error(err)
		}
	})
	assertSnapshotFile(t, acquired.CheckoutDirectory(), "tracked.txt", "working\r\n")
	if _, err := os.Lstat(filepath.Join(acquired.CheckoutDirectory(), "secret.conf")); !os.IsNotExist(err) {
		t.Fatalf("secret present in remote materialization: %v", err)
	}
}

type burstBundleAccess struct{}

func (burstBundleAccess) GitCredential(context.Context, source.Spec) (source.Credential, error) {
	return source.Credential{}, errors.New("bundle must not require Git credentials")
}

func (burstBundleAccess) BundleURL(context.Context, string) (string, error) {
	return "https://example.test/bundle", nil
}

type burstRemoteAccess struct {
	burstBundleAccess
	calls int
	err   error
}

func (a *burstRemoteAccess) GitCredential(context.Context, source.Spec) (source.Credential, error) {
	a.calls++
	return source.Credential{}, a.err
}

func TestBurstBundleRemotePoliciesRequireLiveCredentialAuthority(t *testing.T) {
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "public.txt", "public\n", 0o644)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-m", "base")
	snapshot, err := captureBurstSnapshot(t.Context(), repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	bundle, err := os.ReadFile(snapshot.BundlePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bundle)
	refused := errors.New("source remote authorization refused")
	yes := true
	for _, tc := range []struct {
		name   string
		sha    string
		policy nodeproto.SourceSpec
	}{
		{name: "missing commit", sha: strings.Repeat("b", 40)},
		{name: "tags", policy: nodeproto.SourceSpec{Tags: &yes}},
		{name: "submodules", policy: nodeproto.SourceSpec{Submodules: &yes}},
		{name: "LFS", policy: nodeproto.SourceSpec{LFS: &yes}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sha := snapshot.SHA
			if tc.sha != "" {
				sha = tc.sha
			}
			a := &burstRemoteAccess{err: refused}
			workspace := t.TempDir()
			entry, err := source.PrepareCheckout(t.Context(), source.Spec{RepoKey: "git:example.test/team/repo", RemoteURL: "https://example.test/team/repo.git", SHA: sha, PipelineDir: ".", BundleDigest: hex.EncodeToString(sum[:]), Checkout: runspec.CheckoutPolicy{Policy: tc.policy}}, source.Options{Workspace: workspace, Access: a, BundleTransport: burstBundleTransport{bundle}})
			if entry != nil || !errors.Is(err, refused) || a.calls != 1 {
				t.Fatalf("remote source bypassed authority: entry=%v calls=%d err=%v", entry, a.calls, err)
			}
			files, err := os.ReadDir(workspace)
			if err != nil || len(files) != 0 {
				t.Fatalf("failed source retained workspace: %d %v", len(files), err)
			}
		})
	}
}

type burstBundleTransport struct{ bundle []byte }

func (b burstBundleTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(b.bundle))}, nil
}

func snapshotObjectExists(t *testing.T, checkout, sha string) bool {
	t.Helper()
	cmd := snapshotGitCommand(t.Context(), "-C", checkout, "cat-file", "-e", sha)
	return cmd.Run() == nil
}

func FuzzBurstBundleSecretExclusion(f *testing.F) {
	f.Add("secret", "nested/credentials.conf")
	f.Add("private\x00bytes", "literal[1].txt")
	f.Fuzz(func(t *testing.T, value, name string) {
		if len(value) > 1024 || len(name) > 100 || projectconfig.ValidateSourceFiles([]string{name}) != nil || strings.ContainsAny(name, "\r\n") {
			t.Skip()
		}
		name = "private/" + name
		repo := initSnapshotRepo(t)
		writeSnapshotFile(t, repo, "public.txt", "public\n", 0o644)
		if name == "public.txt" {
			t.Skip()
		}
		writeSnapshotFile(t, repo, name, value, 0o600)
		runSnapshotGit(t, repo, "add", ".")
		runSnapshotGit(t, repo, "commit", "-m", "base")
		snapshot, err := captureBurstSnapshot(t.Context(), repo, []string{name}, []string{name})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := snapshot.close(); err != nil {
				t.Error(err)
			}
		})
		checkout := importSnapshotBundle(t, snapshot)
		if _, err := os.Lstat(filepath.Join(checkout, name)); !os.IsNotExist(err) {
			t.Fatalf("secret path survived: %v", err)
		}
		if value != "public\n" {
			sha := strings.TrimSpace(runSnapshotGitInput(t, repo, value, "hash-object", "--stdin"))
			if snapshotObjectExists(t, checkout, sha) {
				t.Fatal("secret bytes entered bundle")
			}
		}
	})
}

func FuzzBurstSnapshotRejectsSecretValues(f *testing.F) {
	f.Add("opaque-private-value", uint8(0))
	f.Add("quotes\"and spaces/private", uint8(1))
	f.Add("chunk-boundary-private", uint8(2))
	f.Fuzz(func(t *testing.T, value string, form uint8) {
		if len(value) < 8 || len(value) > 1000 {
			t.Skip()
		}
		repo := initSnapshotRepo(t)
		payload := value
		if form%3 == 1 {
			payload = base64.StdEncoding.EncodeToString([]byte(value))
		}
		if form%3 == 2 {
			payload = strings.Repeat("x", (64<<10)-len(value)/2) + value
		}
		writeSnapshotFile(t, repo, "ordinary.dat", payload, 0o644)
		runSnapshotGit(t, repo, "add", ".")
		runSnapshotGit(t, repo, "commit", "-m", "base")
		snapshot, err := captureBurstSnapshot(t.Context(), repo, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := snapshot.close(); err != nil {
				t.Error(err)
			}
		})
		err = snapshot.rejectSecrets(t.Context(), map[string]string{"LOCAL_TOKEN": value})
		if err == nil {
			t.Fatal("secret-bearing bundle admitted")
		}
		if strings.Contains(err.Error(), value) || strings.Contains(err.Error(), "ordinary.dat") {
			t.Fatal("secret scan error reveals private data")
		}
	})
}

func TestBurstSnapshotSecretScanChecksNamesAndAllowsCleanSource(t *testing.T) {
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "private-token-in-filename.txt", "public", 0o644)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-m", "base")
	snapshot, err := captureBurstSnapshot(t.Context(), repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	if err := snapshot.rejectSecrets(t.Context(), map[string]string{"TOKEN": "private-token"}); err == nil {
		t.Fatal("secret in filename admitted")
	}
	if err := snapshot.rejectSecrets(t.Context(), map[string]string{"TOKEN": "absent-private-token"}); err != nil {
		t.Fatal(err)
	}
}

func TestBurstSnapshotPreservesOriginalBranchFacts(t *testing.T) {
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "public.txt", "public\n", 0o644)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-m", "base")
	runSnapshotGit(t, repo, "branch", "-m", "feature")
	sha := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", sha)
	runSnapshotGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	snapshot, err := captureBurstSnapshot(t.Context(), repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	if branch, err := snapshot.branch(t.Context()); err != nil || branch != "feature" {
		t.Fatalf("branch=%q err=%v", branch, err)
	}
	if branch, err := checkoutDefaultBranch(t.Context(), snapshot.RepoRoot); err != nil || branch != "main" {
		t.Fatalf("default branch=%q err=%v", branch, err)
	}
	runSnapshotGit(t, repo, "checkout", "--quiet", "--detach", sha)
	if branch, err := snapshot.branch(t.Context()); err != nil || branch != "" {
		t.Fatalf("detached branch=%q err=%v", branch, err)
	}
	runSnapshotGit(t, repo, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	if branch, err := checkoutDefaultBranch(t.Context(), snapshot.RepoRoot); err != nil || branch != "" {
		t.Fatalf("absent default branch=%q err=%v", branch, err)
	}
	snapshot.RepoRoot = filepath.Join(t.TempDir(), "missing-checkout")
	if _, err := snapshot.branch(t.Context()); err == nil {
		t.Fatal("missing original checkout silently became a detached branch")
	}
	if _, err := checkoutDefaultBranch(t.Context(), snapshot.RepoRoot); err == nil {
		t.Fatal("missing original checkout silently lost its default branch")
	}
}

func TestBurstSourceRejectsUndeclaredPathsAndMissingDeclarations(t *testing.T) {
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "public.txt", "public\n", 0o644)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-m", "base")
	for _, name := range []string{"../outside", "/etc/passwd", ".git/config", "nested/.git/config", ":(glob)*", "missing.txt"} {
		if snapshot, err := captureBurstSnapshot(t.Context(), repo, []string{name}, nil); err == nil {
			if snapshot != nil {
				if err := snapshot.close(); err != nil {
					t.Error(err)
				}
			}
			t.Fatalf("accepted source declaration %q", name)
		}
	}
}
