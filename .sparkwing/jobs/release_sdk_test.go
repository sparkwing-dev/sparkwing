package jobs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func sdkFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "config", "core.hooksPath", "/dev/null")
	gitRun(t, repo, "remote", "add", "origin", repo)
	for name, body := range map[string]string{
		"sparkwing/go.mod":             "module github.com/sparkwing-dev/sparkwing/sparkwing\n\ngo 1.25.0\n",
		"sparkwing/sdk.go":             "package sparkwing\n",
		"sparkwing/assets/message.txt": "embedded\x00\xffdata\n",
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
	sdkFixtureWrite(t, repo, "sparkwing/build/generated.go", "generated output")
	sdkFixtureWrite(t, repo, "sparkwing/go.work.sum", "workspace state")
	sdkFixtureCommit(t, repo)
	unchanged, err := selectSDKRelease(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Changed || unchanged.Version != first.Version || unchanged.Digest != first.Digest {
		t.Fatalf("engine-only = %+v", unchanged)
	}
	for _, name := range []string{"sparkwing/sdk.go", "sparkwing/assets/message.txt", "sparkwing/testdata/case.json", "sparkwing/go.mod", "LICENSE", "sparkwing/testdata/build/input.go", "sparkwing/testdata/dist/embed.txt", "sparkwing/testdata/go.work"} {
		t.Run(name, func(t *testing.T) {
			repo := sdkFixture(t)
			sdkFixtureTag(t, repo, first.Version, first.Digest)
			body := "changed " + name
			if name == "sparkwing/assets/message.txt" {
				body = "embedded\x00\xfedata\n"
			}
			sdkFixtureWrite(t, repo, name, body)
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
	sdkFixtureWrite(t, repo, "sparkwing/sdk.go", "package sparkwing\n")
	if err := os.Remove(filepath.Join(repo, "sparkwing/untracked.txt")); err != nil {
		t.Fatal(err)
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

func TestSDKReleasePublishesBeforeProxyResolutionAndReusesItsTag(t *testing.T) {
	repo := sdkFixture(t)
	sdkFixtureWrite(t, repo, "go.mod", "module fixture/host\n\ngo 1.26.0\nrequire "+sdkModulePath+" v0.1.0\n")
	sdkFixtureWrite(t, repo, "go.sum", "")
	for _, name := range []string{"internal/nodeproto/conformance", "sdk/typescript/test/nativehost"} {
		sdkFixtureWrite(t, repo, name+"/conformance_test.go", "package conformance\nimport (\"testing\"; _ "+strconv.Quote(sdkModulePath)+")\nfunc TestNativeConformance(t *testing.T) {}\n")
	}
	sdkFixtureCommit(t, repo)
	origin := t.TempDir()
	gitRun(t, origin, "init", "--bare", "-b", "main")
	gitRun(t, repo, "remote", "set-url", "origin", origin)
	gitRun(t, repo, "push", "origin", "main")
	source := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	ctx := newTestContext(t)
	digest, err := sdkSourceDigest(ctx, repo, source)
	if err != nil {
		t.Fatal(err)
	}
	sdkFixtureTag(t, repo, "v0.1.0", digest)
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("RELEASE_TEST_CALL", calls)
	t.Setenv("RELEASE_TEST_ORIGIN", origin)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeDeliveryTool(t, filepath.Join(tools, "npm"), "#!/bin/sh\nprintf 'checks\\n' >> \"$RELEASE_TEST_CALL\"\n")
	writeDeliveryTool(t, filepath.Join(tools, "go"), fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$1" = mod ] && [ "$2" = download ]; then
 test "$GOPROXY" = https://proxy.golang.org
 test "$GONOPROXY" = none
 test -z "$(ls -A "$GOMODCACHE")"
 git ls-remote --exit-code --tags "$RELEASE_TEST_ORIGIN" refs/tags/sparkwing/v0.1.0 >/dev/null
 printf 'proxy\n' >> "$RELEASE_TEST_CALL"
 exit 0
fi
exec %s "$@"
`, "'"+strings.ReplaceAll(realGo, "'", "'\"'\"'")+"'"))
	for range 2 {
		version, err := (&releaseSDKJob{RepoDir: repo, Source: source}).run(ctx, false)
		if err != nil || version != "v0.1.0" {
			t.Fatalf("publication = %s, %v", version, err)
		}
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "checks\nproxy\nproxy\n" {
		t.Fatalf("publication calls = %q", body)
	}
	if tagged := strings.TrimSpace(gitRun(t, origin, "rev-parse", "sparkwing/v0.1.0^{commit}")); tagged != source {
		t.Fatalf("SDK tag source = %s, want %s", tagged, source)
	}
	if tags := strings.TrimSpace(gitRun(t, origin, "tag", "--list")); tags != "sparkwing/v0.1.0" {
		t.Fatalf("retry tags = %q", tags)
	}
}
