package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withRepo creates a fresh git repo in a temp dir and returns its
// absolute path. Every helper takes repoDir explicitly, so we no
// longer chdir.
func withRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	runIn(t, dir, "git", "init", "--initial-branch=main", ".")
	runIn(t, dir, "git", "config", "user.email", "test@example.com")
	runIn(t, dir, "git", "config", "user.name", "Test")
	runIn(t, dir, "git", "config", "commit.gpgsign", "false")
	runIn(t, dir, "git", "config", "tag.gpgsign", "false")
	return dir
}

func runIn(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v in %s: %v\n%s", name, args, dir, err, out)
	}
}

func writeFile(t *testing.T, dir, rel, contents string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func commitIn(t *testing.T, dir, msg string) {
	t.Helper()
	runIn(t, dir, "git", "add", "-A")
	runIn(t, dir, "git", "commit", "-m", msg)
}

func TestCurrentSHAAndShortCommit(t *testing.T) {
	dir := withRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	commitIn(t, dir, "init")

	ctx := context.Background()

	sha, err := CurrentSHA(ctx, dir)
	if err != nil {
		t.Fatalf("CurrentSHA: %v", err)
	}
	if len(sha) != 40 {
		t.Fatalf("expected 40-char SHA, got %q", sha)
	}

	short, err := ShortCommit(ctx, dir)
	if err != nil {
		t.Fatalf("ShortCommit: %v", err)
	}
	if len(short) != 12 {
		t.Fatalf("expected 12-char short, got %q", short)
	}
	if !strings.HasPrefix(sha, short) {
		t.Fatalf("short %q is not prefix of full %q", short, sha)
	}
}

func TestCurrentBranch(t *testing.T) {
	dir := withRepo(t)
	writeFile(t, dir, "a.txt", "x")
	commitIn(t, dir, "init")

	ctx := context.Background()

	branch, err := CurrentBranch(ctx, dir)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != "main" {
		t.Fatalf("branch = %q, want main", branch)
	}

	sha, err := CurrentSHA(ctx, dir)
	if err != nil {
		t.Fatalf("CurrentSHA: %v", err)
	}
	runIn(t, dir, "git", "checkout", "--detach", sha)

	branch, err = CurrentBranch(ctx, dir)
	if err != nil {
		t.Fatalf("CurrentBranch detached: %v", err)
	}
	if branch != "" {
		t.Fatalf("detached branch = %q, want empty", branch)
	}
}

func TestRemoteOriginURL(t *testing.T) {
	dir := withRepo(t)
	writeFile(t, dir, "a.txt", "x")
	commitIn(t, dir, "init")

	ctx := context.Background()

	// No origin: returns "" without error.
	url, err := RemoteOriginURL(ctx, dir)
	if err != nil {
		t.Fatalf("RemoteOriginURL no-origin: %v", err)
	}
	if url != "" {
		t.Fatalf("RemoteOriginURL no-origin = %q, want empty", url)
	}

	// Add an origin and re-check.
	runIn(t, dir, "git", "remote", "add", "origin", "git@github.com:owner/repo.git")
	url, err = RemoteOriginURL(ctx, dir)
	if err != nil {
		t.Fatalf("RemoteOriginURL: %v", err)
	}
	if url != "git@github.com:owner/repo.git" {
		t.Fatalf("RemoteOriginURL = %q", url)
	}
}

func TestIsDirty(t *testing.T) {
	dir := withRepo(t)
	writeFile(t, dir, "a.txt", "x")
	commitIn(t, dir, "init")

	ctx := context.Background()

	dirty, err := IsDirty(ctx, dir)
	if err != nil {
		t.Fatalf("IsDirty clean: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean tree, got dirty")
	}

	writeFile(t, dir, "a.txt", "y")
	dirty, err = IsDirty(ctx, dir)
	if err != nil {
		t.Fatalf("IsDirty modified: %v", err)
	}
	if !dirty {
		t.Fatalf("expected dirty tree, got clean")
	}
}

func TestFilesetHashDeterministic(t *testing.T) {
	dir := withRepo(t)
	writeFile(t, dir, "a.txt", "alpha")
	writeFile(t, dir, "sub/b.txt", "beta")
	commitIn(t, dir, "init")

	ctx := context.Background()

	h1, err := FilesetHash(ctx, dir)
	if err != nil {
		t.Fatalf("FilesetHash: %v", err)
	}
	if h1 == "" {
		t.Fatalf("empty hash")
	}
	if len(h1) != 12 {
		t.Fatalf("expected 12 chars, got %d: %q", len(h1), h1)
	}

	h2, err := FilesetHash(ctx, dir)
	if err != nil {
		t.Fatalf("FilesetHash re-run: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("non-deterministic: %q != %q", h1, h2)
	}

	// Untracked-not-ignored files are part of the build context the
	// hash represents, so adding one shifts the hash. Adding the same
	// file to .gitignore puts it back behind the ignore wall.
	writeFile(t, dir, "junk.txt", "not tracked")
	h3, err := FilesetHash(ctx, dir)
	if err != nil {
		t.Fatalf("FilesetHash after untracked: %v", err)
	}
	if h3 == h1 {
		t.Fatalf("untracked file did not change hash: %q == %q", h3, h1)
	}
	writeFile(t, dir, ".gitignore", "junk.txt\n")
	h3b, err := FilesetHash(ctx, dir)
	if err != nil {
		t.Fatalf("FilesetHash after gitignore: %v", err)
	}
	// .gitignore is itself an untracked file, so the hash differs from
	// h1. The point is that h3b reflects "junk.txt is ignored": removing
	// junk.txt from disk should leave the hash unchanged.
	if err := os.Remove(filepath.Join(dir, "junk.txt")); err != nil {
		t.Fatalf("remove junk.txt: %v", err)
	}
	h3c, err := FilesetHash(ctx, dir)
	if err != nil {
		t.Fatalf("FilesetHash after gitignore+remove: %v", err)
	}
	if h3b != h3c {
		t.Fatalf("ignored file removal shifted hash: %q != %q", h3b, h3c)
	}

	writeFile(t, dir, "a.txt", "alpha-v2")
	h4, err := FilesetHash(ctx, dir)
	if err != nil {
		t.Fatalf("FilesetHash after edit: %v", err)
	}
	if h4 == h3c {
		t.Fatalf("edit to a.txt did not affect hash")
	}
}

func TestFilesetHashRespectsDockerignore(t *testing.T) {
	dir := withRepo(t)
	writeFile(t, dir, "a.txt", "alpha")
	writeFile(t, dir, "secret.env", "API_KEY=1")
	commitIn(t, dir, "init")

	ctx := context.Background()
	h1, err := FilesetHash(ctx, dir)
	if err != nil {
		t.Fatalf("FilesetHash: %v", err)
	}

	writeFile(t, dir, ".dockerignore", "secret.env\n")
	h2, err := FilesetHash(ctx, dir)
	if err != nil {
		t.Fatalf("FilesetHash with dockerignore: %v", err)
	}
	// .dockerignore itself is in the fileset, so the hash changes.
	if h1 == h2 {
		t.Fatalf(".dockerignore had no effect: %q == %q", h1, h2)
	}

	if err := os.WriteFile(filepath.Join(dir, "secret.env"), []byte("API_KEY=2"), 0o644); err != nil {
		t.Fatalf("rewrite secret: %v", err)
	}
	h3, err := FilesetHash(ctx, dir)
	if err != nil {
		t.Fatalf("FilesetHash after secret edit: %v", err)
	}
	if h2 != h3 {
		t.Fatalf("dockerignored file leaked into hash: %q != %q", h2, h3)
	}
}

func TestFilesetHashFilesystemFallback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "alpha")
	writeFile(t, dir, "sub/b.txt", "beta")

	ctx := context.Background()
	h1, err := FilesetHash(ctx, dir)
	if err != nil {
		t.Fatalf("FilesetHash no-git: %v", err)
	}
	if h1 == "" {
		t.Fatalf("empty hash from filesystem walk")
	}

	writeFile(t, dir, "a.txt", "alpha-v2")
	h2, err := FilesetHash(ctx, dir)
	if err != nil {
		t.Fatalf("FilesetHash no-git after edit: %v", err)
	}
	if h1 == h2 {
		t.Fatalf("filesystem walk did not pick up edit")
	}
}

func TestChangedFiles(t *testing.T) {
	dir := withRepo(t)
	writeFile(t, dir, "a.txt", "alpha")
	writeFile(t, dir, "b.txt", "beta")
	commitIn(t, dir, "init")
	first, err := CurrentSHA(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "a.txt", "alpha2")
	writeFile(t, dir, "c.txt", "charlie")
	commitIn(t, dir, "second")

	ctx := context.Background()
	files, err := ChangedFiles(ctx, dir, first)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	got := strings.Join(files, ",")
	if !strings.Contains(got, "a.txt") || !strings.Contains(got, "c.txt") {
		t.Fatalf("expected a.txt + c.txt in %q", got)
	}
	if strings.Contains(got, "b.txt") {
		t.Fatalf("b.txt unchanged should not appear in %q", got)
	}
}

func TestTagsAtHead(t *testing.T) {
	dir := withRepo(t)
	writeFile(t, dir, "a.txt", "x")
	commitIn(t, dir, "init")

	ctx := context.Background()

	tags, err := TagsAtHead(ctx, dir)
	if err != nil {
		t.Fatalf("TagsAtHead empty: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("expected no tags, got %v", tags)
	}

	runIn(t, dir, "git", "tag", "-a", "v0.1.0", "-m", "first")
	runIn(t, dir, "git", "tag", "-a", "release-1", "-m", "alias")

	tags, err = TagsAtHead(ctx, dir)
	if err != nil {
		t.Fatalf("TagsAtHead: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %v", tags)
	}
}

func TestLatestTagSemverOrdering(t *testing.T) {
	dir := withRepo(t)
	writeFile(t, dir, "a.txt", "x")
	commitIn(t, dir, "init")

	for _, tag := range []string{"v0.2.0", "v0.10.0", "v0.9.9", "v0.1.0"} {
		runIn(t, dir, "git", "tag", tag)
	}
	runIn(t, dir, "git", "tag", "vNext")
	runIn(t, dir, "git", "tag", "release/v2.0.0")

	ctx := context.Background()

	got, err := LatestTag(ctx, dir, "v")
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if got != "v0.10.0" {
		t.Fatalf("LatestTag(v) = %q, want v0.10.0", got)
	}

	got, err = LatestTag(ctx, dir, "release/v")
	if err != nil {
		t.Fatalf("LatestTag release/v: %v", err)
	}
	if got != "release/v2.0.0" {
		t.Fatalf("LatestTag(release/v) = %q, want release/v2.0.0", got)
	}

	got, err = LatestTag(ctx, dir, "nothing-")
	if err != nil {
		t.Fatalf("LatestTag miss: %v", err)
	}
	if got != "" {
		t.Fatalf("LatestTag miss = %q, want empty", got)
	}
}

func TestPushTagRefusesExisting(t *testing.T) {
	dir := withRepo(t)

	remote := filepath.Join(filepath.Dir(dir), "origin.git")
	runIn(t, "", "git", "init", "--bare", remote)
	runIn(t, dir, "git", "remote", "add", "origin", remote)

	writeFile(t, dir, "a.txt", "x")
	commitIn(t, dir, "init")
	runIn(t, dir, "git", "push", "-u", "origin", "main")

	ctx := context.Background()

	if err := PushTag(ctx, dir, "v1.0.0", "first cut"); err != nil {
		t.Fatalf("PushTag first: %v", err)
	}

	exists, err := TagExistsOnRemote(ctx, dir, "v1.0.0")
	if err != nil {
		t.Fatalf("TagExistsOnRemote: %v", err)
	}
	if !exists {
		t.Fatal("v1.0.0 should exist on remote after push")
	}

	runIn(t, dir, "git", "tag", "-d", "v1.0.0")

	err = PushTag(ctx, dir, "v1.0.0", "second cut")
	if err == nil {
		t.Fatalf("PushTag repeat: expected error, got nil")
	}
	if !errors.Is(err, ErrTagAlreadyExists) {
		t.Fatalf("PushTag repeat: got %v, want ErrTagAlreadyExists", err)
	}

	if err := PushTag(ctx, dir, "v1.0.1", "increment"); err != nil {
		t.Fatalf("PushTag new: %v", err)
	}
}

// TestNoEnvFallback_OutsideGitRepo: env vars must never short-circuit
// real git output. Even with SPARKWING_COMMIT/BRANCH set, calls
// against a non-repo dir error out.
func TestNoEnvFallback_OutsideGitRepo(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("SPARKWING_COMMIT", "deadbeefcafe1234deadbeefcafe1234deadbeef")
	t.Setenv("SPARKWING_BRANCH", "fake-branch")

	ctx := context.Background()

	if sha, err := CurrentSHA(ctx, dir); err == nil {
		t.Errorf("CurrentSHA outside repo: want error, got %q", sha)
	}
	if sha, err := ShortCommit(ctx, dir); err == nil {
		t.Errorf("ShortCommit outside repo: want error, got %q", sha)
	}
	if br, err := CurrentBranch(ctx, dir); err == nil {
		t.Errorf("CurrentBranch outside repo: want error, got %q", br)
	}
	if dirty, err := IsDirty(ctx, dir); err == nil {
		t.Errorf("IsDirty outside repo: want error, got %v", dirty)
	}
}

func TestPushTagRejectsEmpty(t *testing.T) {
	dir := withRepo(t)
	writeFile(t, dir, "a.txt", "x")
	commitIn(t, dir, "init")

	if err := PushTag(context.Background(), dir, "", "msg"); err == nil {
		t.Fatalf("PushTag(\"\") = nil, want error")
	}
}
