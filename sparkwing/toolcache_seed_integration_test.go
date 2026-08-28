package sparkwing_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func copyOfCache(t *testing.T, src, dst string) string {
	t.Helper()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatalf("copy cache %s -> %s: %v", src, dst, err)
	}
	return dst
}
