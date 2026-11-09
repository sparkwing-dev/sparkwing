package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sdkFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "remote", "add", "origin", repo)
	for name, body := range map[string]string{
		"sparkwing/go.mod":             "module github.com/sparkwing-dev/sparkwing/sparkwing\n\ngo 1.25.0\n",
		"sparkwing/sdk.go":             "package sparkwing\n",
		"sparkwing/assets/message.txt": "embedded\x00data\n",
		"sparkwing/testdata/case.json": "{}\n",
		"LICENSE":                      "license\n",
		"go.mod":                       "module engine\n",
	} {
		sdkFixtureWrite(t, repo, name, body)
	}
	sdkFixtureCommit(t, repo)
	return repo
}

func sdkFixtureWrite(t *testing.T, repo, name, body string) {
	t.Helper()
	target := filepath.Join(repo, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, target, body)
}

func sdkFixtureCommit(t *testing.T, repo string) {
	t.Helper()
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--no-verify", "-m", "fixture")
}

func sdkFixtureTag(t *testing.T, repo, version, digest string) {
	t.Helper()
	gitRun(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "tag.gpgSign=false", "tag", "-a", "sparkwing/"+version, "-m", "SDK\n\n"+sdkDigestMarker+digest)
}

func TestSDKReleaseSelection(t *testing.T) {
	repo := sdkFixture(t)
	ctx := newTestContext(t)
	first, err := selectSDKRelease(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || first.Version != "v0.1.0" || len(first.Digest) != 64 {
		t.Fatalf("first = %+v", first)
	}
	sdkFixtureTag(t, repo, first.Version, first.Digest)
	sdkFixtureWrite(t, repo, "go.mod", "module changedengine\n")
	sdkFixtureWrite(t, repo, "sparkwing/go.work", "workspace state")
	sdkFixtureWrite(t, repo, "sparkwing/dist/output", "generated output")
	sdkFixtureCommit(t, repo)
	unchanged, err := selectSDKRelease(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Changed || unchanged.Version != first.Version || unchanged.Digest != first.Digest {
		t.Fatalf("engine-only = %+v", unchanged)
	}
	for _, name := range []string{"sparkwing/sdk.go", "sparkwing/assets/message.txt", "sparkwing/testdata/case.json", "sparkwing/go.mod", "LICENSE"} {
		t.Run(name, func(t *testing.T) {
			sdkFixtureWrite(t, repo, name, "changed "+name)
			sdkFixtureCommit(t, repo)
			next, err := selectSDKRelease(ctx, repo, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if !next.Changed || next.Version != "v0.2.0" || next.Digest == first.Digest {
				t.Fatalf("changed = %+v", next)
			}
		})
	}
}

func TestSDKReleaseImmutableTagDigest(t *testing.T) {
	repo := sdkFixture(t)
	ctx := newTestContext(t)
	digest, err := sdkSourceDigest(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sdkFixtureTag(t, repo, "v0.1.0", digest)
	retry, err := selectSDKRelease(ctx, repo, "HEAD")
	if err != nil || retry.Changed {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
	sdkFixtureTag(t, repo, "v0.2.0", strings.Repeat("0", 64))
	if _, err := selectSDKRelease(ctx, repo, "HEAD"); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("corrupt recorded digest = %v", err)
	}
}

func TestSDKSourceDigestUsesTrackedCommitAndDetectsDeletion(t *testing.T) {
	repo := sdkFixture(t)
	ctx := newTestContext(t)
	before, err := sdkSourceDigest(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sdkFixtureWrite(t, repo, "sparkwing/sdk.go", "uncommitted")
	sdkFixtureWrite(t, repo, "sparkwing/untracked.txt", "untracked")
	after, err := sdkSourceDigest(ctx, repo, "HEAD")
	if err != nil || before != after {
		t.Fatalf("checkout state changed digest: %s %s %v", before, after, err)
	}
	gitRun(t, repo, "rm", "sparkwing/testdata/case.json")
	sdkFixtureCommit(t, repo)
	deleted, err := sdkSourceDigest(ctx, repo, "HEAD")
	if err != nil || deleted == before {
		t.Fatalf("deletion not detected: %s %v", deleted, err)
	}
}

func TestSDKReleaseRefusesConflictingLocalTag(t *testing.T) {
	origin := sdkFixture(t)
	ctx := newTestContext(t)
	digest, err := sdkSourceDigest(ctx, origin, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sdkFixtureTag(t, origin, "v0.1.0", digest)
	checkout := filepath.Join(t.TempDir(), "checkout")
	gitRun(t, origin, "clone", "--no-tags", origin, checkout)
	sdkFixtureTag(t, checkout, "v0.1.0", strings.Repeat("0", 64))
	if _, err := selectSDKRelease(ctx, checkout, "HEAD"); err == nil || !strings.Contains(err.Error(), "immutable SDK tag") {
		t.Fatalf("conflicting local tag = %v", err)
	}
}

func TestSDKReleasePreviewDoesNotPublish(t *testing.T) {
	repo := sdkFixture(t)
	version, err := (&releaseSDKJob{RepoDir: repo, Source: "HEAD"}).run(newTestContext(t), true)
	if err != nil || version != "v0.1.0" {
		t.Fatalf("preview = %s, %v", version, err)
	}
	if tags := gitRun(t, repo, "tag", "--list"); tags != "" {
		t.Fatalf("preview created tags: %s", tags)
	}
}
