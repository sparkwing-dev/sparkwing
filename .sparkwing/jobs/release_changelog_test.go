package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnreleasedEntries(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			name: "empty section, only blank lines",
			body: "# Changelog\n\n## [Unreleased]\n\n## [v1.0.0]\n\n- old entry\n",
			want: 0,
		},
		{
			name: "empty section, only sub-headings",
			body: "# Changelog\n\n## [Unreleased]\n\n### Added\n\n### Changed\n\n## [v1.0.0]\n",
			want: 0,
		},
		{
			name: "one entry under Added",
			body: "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- new thing\n\n## [v1.0.0]\n",
			want: 1,
		},
		{
			name: "entries across multiple sub-sections",
			body: "## [Unreleased]\n### Added\n- a\n- b\n### Fixed\n- c\n## [v1.0.0]\n- old\n",
			want: 3,
		},
		{
			name: "no [Unreleased] section",
			body: "# Changelog\n\n## [v1.0.0]\n\n- something\n",
			want: 0,
		},
		{
			name: "bare 'Unreleased' (no brackets) also recognized",
			body: "## Unreleased\n\n- entry\n\n## [v1.0.0]\n",
			want: 1,
		},
		{
			name: "entries in old version do not count",
			body: "## [Unreleased]\n\n## [v1.0.0]\n- old\n- older\n",
			want: 0,
		},
		{
			name: "stops at next top-level heading, ignores indented dashes",
			body: "## [Unreleased]\n\n### Added\n  -- this is not a bullet, dashed prose\n- real bullet\n## [v1.0.0]\n",
			want: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := unreleasedEntries(c.body)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %d entries, want %d", got, c.want)
			}
		})
	}
}

func TestVersionEntries(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		version string
		want    int
	}{
		{
			name:    "version section with three entries",
			body:    "## [Unreleased]\n\n## [v1.0.0] - 2026-05-20\n\n### Added\n- a\n- b\n### Fixed\n- c\n",
			version: "v1.0.0",
			want:    3,
		},
		{
			name:    "version section without date",
			body:    "## [Unreleased]\n\n## [v1.0.0]\n\n- a\n- b\n",
			version: "v1.0.0",
			want:    2,
		},
		{
			name:    "absent version",
			body:    "## [Unreleased]\n- entry\n## [v0.9.0]\n- old\n",
			version: "v1.0.0",
			want:    0,
		},
		{
			name:    "stops at next ## heading",
			body:    "## [v1.0.0]\n- a\n## [v0.9.0]\n- old\n- older\n",
			version: "v1.0.0",
			want:    1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := versionEntries(c.body, c.version)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %d entries, want %d", got, c.want)
			}
		})
	}
}

func TestRewriteUnreleasedToVersion(t *testing.T) {
	body := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- thing one\n\n## [v0.9.0]\n\n- prior\n"
	out, err := rewriteUnreleasedToVersion(body, "v1.0.0", "2026-05-20")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "## [Unreleased]") {
		t.Errorf("expected fresh [Unreleased] heading, got:\n%s", out)
	}
	if !strings.Contains(out, "## [v1.0.0] - 2026-05-20") {
		t.Errorf("expected [v1.0.0] - 2026-05-20 heading, got:\n%s", out)
	}
	idxVer := strings.Index(out, "## [v1.0.0]")
	idxThing := strings.Index(out, "- thing one")
	idxOld := strings.Index(out, "## [v0.9.0]")
	if !(idxVer < idxThing && idxThing < idxOld) {
		t.Fatalf("expected `- thing one` between [v1.0.0] and [v0.9.0]; positions ver=%d thing=%d old=%d\n%s",
			idxVer, idxThing, idxOld, out)
	}
}

func TestRewriteUnreleasedToVersion_NoUnreleasedHeading(t *testing.T) {
	body := "# Changelog\n\n## [v1.0.0]\n\n- old\n"
	if _, err := rewriteUnreleasedToVersion(body, "v1.0.1", "2026-05-20"); err == nil {
		t.Fatal("expected error when [Unreleased] absent, got nil")
	}
}

func TestValidateReleaseVersion_Pre1Lock(t *testing.T) {
	cases := []struct {
		version string
		wantErr string
	}{
		{version: "v0.1.0", wantErr: ""},
		{version: "v0.6.1", wantErr: ""},
		{version: "v0.99.999", wantErr: ""},
		{version: "v1.0.0", wantErr: "pre-1.0 lock"},
		{version: "v1.2.3", wantErr: "pre-1.0 lock"},
		{version: "v2.0.0", wantErr: "pre-1.0 lock"},
	}
	for _, c := range cases {
		t.Run(c.version, func(t *testing.T) {
			err := validateReleaseVersion(c.version)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestHighestReleaseTag(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{
			name: "skips retracted v1.x tombstone and picks highest v0.x",
			tags: []string{"v0.9.1", "v0.10.0", "v0.11.0", "v1.6.1"},
			want: "v0.11.0",
		},
		{
			name: "v1.0.0 ceiling is exclusive",
			tags: []string{"v0.11.0", "v1.0.0"},
			want: "v0.11.0",
		},
		{
			name: "ignores pre-release and build metadata",
			tags: []string{"v0.11.0", "v0.12.0-rc1", "v0.12.0+build"},
			want: "v0.11.0",
		},
		{
			name: "ignores non-semver refs",
			tags: []string{"v0.11.0", "latest", "release", "v0.x"},
			want: "v0.11.0",
		},
		{
			name: "no eligible tag yields empty",
			tags: []string{"v1.6.1", "v2.0.0"},
			want: "",
		},
		{
			name: "empty input yields empty",
			tags: nil,
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := highestReleaseTag(c.tags); got != c.want {
				t.Fatalf("highestReleaseTag(%v) = %q, want %q", c.tags, got, c.want)
			}
		})
	}
}

func TestEnsureBranchContainsRemote(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	clone := filepath.Join(root, "clone")

	runTestGit(t, root, "init", "--bare", remote)
	runTestGit(t, root, "clone", remote, work)
	runTestGit(t, work, "config", "user.name", "Test User")
	runTestGit(t, work, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, work, "add", "README.md")
	runTestGit(t, work, "commit", "-m", "initial")
	runTestGit(t, work, "branch", "-M", "release-test")
	runTestGit(t, work, "push", "-u", "origin", "release-test")

	if err := ensureBranchContainsRemote(ctx, work, "release-test"); err != nil {
		t.Fatalf("fresh release branch rejected: %v", err)
	}

	runTestGit(t, root, "clone", remote, clone)
	runTestGit(t, clone, "checkout", "release-test")
	runTestGit(t, clone, "config", "user.name", "Test User")
	runTestGit(t, clone, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, clone, "add", "README.md")
	runTestGit(t, clone, "commit", "-m", "advance remote")
	runTestGit(t, clone, "push", "origin", "HEAD:release-test")

	if err := ensureBranchContainsRemote(ctx, work, "release-test"); err == nil {
		t.Fatalf("stale release branch passed freshness fence")
	}
}

func TestWriteSelfModuleSums(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	writeSelfModuleSumsFixture(t, repo)

	const version = "v0.1.0"
	zipHash, goModHash, err := selfModuleSums(context.Background(), repo, version)
	if err != nil {
		t.Fatalf("selfModuleSums: %v", err)
	}
	assertWriteSelfModuleSums(t, repo, version, zipHash, goModHash)
}

func TestWriteSelfModuleSumsInGitWorktree(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	writeSelfModuleSumsFixture(t, repo)
	runTestGit(t, repo, "branch", "release-line")

	worktree := filepath.Join(tmp, "release-worktree")
	runTestGit(t, repo, "worktree", "add", worktree, "release-line")

	const version = "v0.1.0"
	zipHash, goModHash, err := selfModuleSums(context.Background(), worktree, version)
	if err != nil {
		t.Fatalf("selfModuleSums: %v", err)
	}
	assertWriteSelfModuleSums(t, worktree, version, zipHash, goModHash)
}

func writeSelfModuleSumsFixture(t *testing.T, repo string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repo, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":              "module github.com/sparkwing-dev/sparkwing\n\ngo 1.26.0\n",
		"main.go":             "package sparkwing\n",
		".sparkwing/go.sum":   "github.com/sparkwing-dev/sparkwing v0.1.0 h1:stale\n",
		".sparkwing/go.mod":   "module sparkwing-pipelines\n\ngo 1.26.0\n",
		".gitignore":          "",
		"docs/placeholder.md": "# Placeholder\n",
	}
	for name, body := range files {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runTestGit(t, repo, "init")
	runTestGit(t, repo, "config", "user.name", "Test User")
	runTestGit(t, repo, "config", "user.email", "test@example.com")
	runTestGit(t, repo, "add", ".")
	runTestGit(t, repo, "commit", "-m", "initial")
}

func assertWriteSelfModuleSums(t *testing.T, repo, version, zipHash, goModHash string) {
	t.Helper()
	if err := writeSelfModuleSums(context.Background(), repo, version); err != nil {
		t.Fatalf("writeSelfModuleSums: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(repo, ".sparkwing", "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		sparkwingModulePath + " " + version + " " + zipHash,
		sparkwingModulePath + " " + version + "/go.mod " + goModHash,
	} {
		if !strings.Contains(string(first), want) {
			t.Fatalf(".sparkwing/go.sum missing %q:\n%s", want, first)
		}
	}
	if strings.Contains(string(first), "h1:stale") {
		t.Fatalf(".sparkwing/go.sum kept stale self-module sum:\n%s", first)
	}

	if err := writeSelfModuleSums(context.Background(), repo, version); err != nil {
		t.Fatalf("repeat writeSelfModuleSums: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(repo, ".sparkwing", "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("second write changed go.sum:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestPlanChangelogRewrite(t *testing.T) {
	const date = "2026-05-20"
	cases := []struct {
		name     string
		body     string
		version  string
		wantKind changelogRewriteKind
		wantErr  string
		wantBody string
	}{
		{
			name:     "apply when Unreleased has content and version absent",
			body:     "## [Unreleased]\n\n### Added\n- thing\n",
			version:  "v1.0.0",
			wantKind: rewriteApply,
			wantBody: "## [v1.0.0] - ",
		},
		{
			name:     "noop when version already populated and Unreleased empty",
			body:     "## [Unreleased]\n\n## [v1.0.0] - 2026-05-20\n- thing\n",
			version:  "v1.0.0",
			wantKind: rewriteNoop,
		},
		{
			name:    "refuse when both Unreleased and version populated",
			body:    "## [Unreleased]\n- new\n## [v1.0.0]\n- already\n",
			version: "v1.0.0",
			wantErr: "BOTH [Unreleased]",
		},
		{
			name:    "refuse when nothing to ship",
			body:    "## [Unreleased]\n\n## [v0.9.0]\n- old\n",
			version: "v1.0.0",
			wantErr: "[Unreleased] is empty",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := planChangelogRewrite(c.body, c.version)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.kind != c.wantKind {
				t.Fatalf("kind = %v, want %v", got.kind, c.wantKind)
			}
			if c.wantBody != "" && !strings.Contains(got.newBody, c.wantBody) {
				t.Fatalf("newBody missing %q:\n%s", c.wantBody, got.newBody)
			}
			_ = date
		})
	}
}
