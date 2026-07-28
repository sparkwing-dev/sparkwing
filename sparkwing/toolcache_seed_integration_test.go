package sparkwing_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Seeding a new worktree's cache from one an earlier worktree filled is the
// obvious way to skip a cold start, and it is not sound. golangci-lint stores
// an issue with the absolute path of the tree that produced it and keys it on
// file content alone, so a copied cache replays the donor's paths into any
// worktree it is restored into -- the failure a per-worktree
// ToolCacheDir exists to prevent, reached by a different route.
//
// The obvious guard, seeding only from a run that reported nothing, does not
// hold either: exclusion rules and diff baselines are applied when results are
// REPORTED, while the cache stores what the analyzers RETURNED. A run can
// print "0 issues" and still leave issues in its cache, each carrying the
// donor's absolute path.
func TestSeededToolCache_ReportsTheDonorsPathsWhenTheDonorRunHadFindings(t *testing.T) {
	root := lintFixtureRoot(t)
	donor := seedLintWorktree(t, filepath.Join(root, "donor"))
	target := seedLintWorktree(t, filepath.Join(root, "target"))

	donorCache := filepath.Join(root, "cache-donor")
	if out := lintWithCache(t, donor, donorCache); !strings.Contains(out, donor) {
		t.Fatalf("donor lint did not report its own tree:\n%s", out)
	}

	out := lintWithCache(t, target, copyOfCache(t, donorCache, filepath.Join(root, "cache-seeded")))
	if !strings.Contains(out, donor) {
		t.Fatalf("seeding from a donor that had findings no longer replays the donor's "+
			"paths; the hazard this pins may have changed shape:\n%s", out)
	}
}

// The donor here reports nothing because its config excludes the only finding,
// which is the shape every repo carrying exclusion rules or a merge-base
// baseline is in. The donor tree is deleted before the seeded run, because a
// per-ticket worktree is deleted when the ticket lands and a path into it stops
// resolving; that is what turns a silently wrong path into a visible one.
func TestSeededToolCache_NamesTheDonorEvenWhenTheDonorRunReportedNothing(t *testing.T) {
	root := lintFixtureRoot(t)
	donor := seedLintWorktree(t, filepath.Join(root, "donor"))
	target := seedLintWorktree(t, filepath.Join(root, "target"))
	config := writeExcludeAllConfig(t, root)

	donorCache := filepath.Join(root, "cache-donor")
	if out := lintWithCacheConfig(t, donor, donorCache, config); strings.Contains(out, ".go:") {
		t.Fatalf("donor was meant to report nothing, so this fixture no longer "+
			"exercises the excluded-but-cached case:\n%s", out)
	}

	seeded := copyOfCache(t, donorCache, filepath.Join(root, "cache-seeded"))
	if err := os.RemoveAll(donor); err != nil {
		t.Fatalf("remove donor: %v", err)
	}

	out := lintWithCacheConfig(t, target, seeded, config)
	if !strings.Contains(out, donor) {
		t.Fatalf("a cache saved from a run that reported nothing no longer names "+
			"the donor; seeding may have become safe, which would change the "+
			"design this pins:\n%s", out)
	}
}

// lintFixtureRoot returns a temp root for a seeding fixture. A missing
// toolchain skips the test rather than passing it: these assertions are
// about golangci-lint's own cache behavior, so without golangci-lint they
// establish nothing.
func lintFixtureRoot(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("runs golangci-lint twice")
	}
	for _, bin := range []string{"golangci-lint", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	return t.TempDir()
}

// writeExcludeAllConfig writes a config that excludes the fixture's only
// finding, so the analyzer still runs and its issue is still cached while the
// report comes back empty. Returns the config path.
func writeExcludeAllConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "golangci.yml")
	body := "version: \"2\"\n" +
		"linters:\n" +
		"  default: standard\n" +
		"  exclusions:\n" +
		"    rules:\n" +
		"      - path: unused\\.go\n" +
		"        linters:\n" +
		"          - unused\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// copyOfCache duplicates a filled cache directory the way a restore from a
// blob store would, and returns the copy.
func copyOfCache(t *testing.T, src, dst string) string {
	t.Helper()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("copy cache %s -> %s: %v", src, dst, err)
	}
	return dst
}
