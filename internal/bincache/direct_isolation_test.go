package bincache

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDirectCheckoutOwnsItsGitObjects(t *testing.T) {
	repos := t.TempDir()
	old, tip := makeBareRepoWithSparkwing(t, repos, "upstream", "main")
	remote := filepath.Join(repos, "upstream.git")
	root := t.TempDir()
	t.Setenv("SPARKWING_HOME", root)
	for _, sha := range []string{old, tip} {
		dest := filepath.Join(t.TempDir(), "src")
		if err := directCheckout(t.Context(), remote, "main", sha, dest, directOptions{protocols: "file"}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(dest, ".git"))
		if err != nil || !info.IsDir() {
			t.Fatalf("checkout must own its git directory: %v, %v", info, err)
		}
		if marker := readMarker(t, dest); marker != map[string]string{old: "v1", tip: "v2"}[sha] {
			t.Fatalf("wrong tree for %s: %s", sha, marker)
		}
		if got := gitOut(t, dest, "rev-parse", "HEAD"); got != sha {
			t.Fatalf("HEAD = %s, want %s", got, sha)
		}
		if _, err := os.Stat(filepath.Join(dest, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
			t.Fatalf("checkout borrows objects: %v", err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("fetch left shared storage: %v, %v", entries, err)
	}
}

func TestDirectCheckoutRefetchesFromUpstream(t *testing.T) {
	repos := t.TempDir()
	sha, _ := makeBareRepoWithSparkwing(t, repos, "upstream", "main")
	remote := filepath.Join(repos, "upstream.git")
	opts := directOptions{protocols: "file"}
	first := filepath.Join(t.TempDir(), "first")
	if err := directCheckout(t.Context(), remote, "main", sha, first, opts); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(remote, remote+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "second")
	if err := directCheckout(t.Context(), remote, "main", sha, dest, opts); err == nil {
		t.Fatal("unavailable upstream was satisfied by shared source storage")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("failed fetch left a checkout: %v", err)
	}
	if got := readMarker(t, first); got != "v1" {
		t.Fatalf("earlier checkout changed: %s", got)
	}
}

func TestDirectCheckoutSelectsUpstreamRefs(t *testing.T) {
	repos := t.TempDir()
	old, tip := makeBareRepoWithSparkwing(t, repos, "upstream", "main")
	remote := filepath.Join(repos, "upstream.git")
	gitOut(t, remote, "update-ref", "refs/tags/release", old)
	for _, tc := range []struct {
		name, branch, ref, sha, want string
	}{
		{name: "branch", branch: "main", want: tip},
		{name: "declared branch", ref: "refs/heads/main", want: tip},
		{name: "declared tag", ref: "refs/tags/release", want: old},
		{name: "declared commit", ref: old, want: old},
		{name: "exact commit", branch: "main", sha: old, want: old},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "src")
			opts := directOptions{protocols: "file", sourceRef: tc.ref}
			if err := directCheckout(t.Context(), remote, tc.branch, tc.sha, dest, opts); err != nil {
				t.Fatal(err)
			}
			if got := gitOut(t, dest, "rev-parse", "HEAD"); got != tc.want {
				t.Fatalf("HEAD = %s, want %s", got, tc.want)
			}
			if got := gitOut(t, dest, "remote", "get-url", "origin"); got != remote {
				t.Fatalf("origin = %q, want %q", got, remote)
			}
		})
	}
}

func TestDirectCheckoutPreservesExistingDestination(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "directory", true: "symlink"}[symlink], func(t *testing.T) {
			dest, contents := t.TempDir(), t.TempDir()
			if symlink {
				dest = filepath.Join(dest, "checkout")
				if err := os.Symlink(contents, dest); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else {
				contents = dest
			}
			writeTestFile(t, filepath.Join(contents, "keep"), "original")
			if err := directCheckout(t.Context(), "missing", "main", testSHA1, dest, directOptions{protocols: "file"}); err == nil {
				t.Fatal("existing destination accepted")
			}
			body, err := os.ReadFile(filepath.Join(contents, "keep"))
			if err != nil || string(body) != "original" {
				t.Fatalf("existing destination changed: %q, %v", body, err)
			}
		})
	}
}

func TestDirectCheckoutCancelsBeforeCreation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dest := filepath.Join(t.TempDir(), "src")
	if err := directCheckout(ctx, "missing", "main", testSHA1, dest, directOptions{protocols: "file"}); err != context.Canceled {
		t.Fatalf("canceled checkout = %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("canceled checkout created files: %v", err)
	}
}

func TestDirectCheckoutConcurrentFetchesOwnTheirObjects(t *testing.T) {
	repos := t.TempDir()
	sha, _ := makeBareRepoWithSparkwing(t, repos, "upstream", "main")
	remote := filepath.Join(repos, "upstream.git")
	root := t.TempDir()
	destinations := []string{filepath.Join(root, "first"), filepath.Join(root, "second")}
	var wg sync.WaitGroup
	for _, dest := range destinations {
		wg.Go(func() {
			if err := directCheckout(t.Context(), remote, "main", sha, dest, directOptions{protocols: "file"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	if err := os.RemoveAll(destinations[0]); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, destinations[1], "show", "HEAD:.sparkwing/marker"); got != "v1" {
		t.Fatalf("independent checkout lost its Git objects: %s", got)
	}
}

func TestDirectCheckoutIgnoresGitTemplates(t *testing.T) {
	repos := t.TempDir()
	sha, _ := makeBareRepoWithSparkwing(t, repos, "upstream", "main")
	template := t.TempDir()
	writeTestFile(t, filepath.Join(template, "injected"), "ambient template")
	t.Setenv("GIT_TEMPLATE_DIR", template)
	dest := filepath.Join(t.TempDir(), "src")
	if err := directCheckout(t.Context(), filepath.Join(repos, "upstream.git"), "main", sha, dest, directOptions{protocols: "file"}); err != nil {
		t.Fatal(err)
	}
	gitdir := gitOut(t, dest, "rev-parse", "--absolute-git-dir")
	if _, err := os.Stat(filepath.Join(strings.TrimSpace(gitdir), "injected")); !os.IsNotExist(err) {
		t.Fatalf("ambient template copied into checkout: %v", err)
	}
}
