package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalBehindRemoteDistinguishesGitFailures(t *testing.T) {
	for _, kind := range []string{"outside checkout", "git unavailable", "stale linked worktree", "malformed metadata", "nested malformed metadata", "unreadable directory", "cancelled context"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			ctx := context.Background()
			if kind != "outside checkout" {
				versionFixtureGit(t, root, "init", "--quiet")
			}
			switch kind {
			case "git unavailable":
				t.Setenv("PATH", t.TempDir())
			case "stale linked worktree":
				if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+filepath.Join(root, "missing-repo", "worktrees", "stale")+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "malformed metadata", "nested malformed metadata":
				if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
				if kind == "nested malformed metadata" {
					root = filepath.Join(root, "module", "nested")
					if err := os.MkdirAll(root, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			case "unreadable directory":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses directory permissions")
				}
				if err := os.Chmod(root, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(root, 0o700); err != nil {
						t.Error(err)
					}
				})
			case "cancelled context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			behind, by, err := localBehindRemote(ctx, root)
			if kind == "outside checkout" {
				if err != nil || behind || by != 0 {
					t.Fatalf("noncheckout = %v, %d, %v", behind, by, err)
				}
			} else if err == nil {
				t.Fatalf("Git failure produced false clean: behind=%v by=%d err=nil", behind, by)
			}
		})
	}
}

func versionFixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "GIT_") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestLocalBehindRemotePreservesNonApplicableOrigins(t *testing.T) {
	for _, kind := range []string{"no origin", "missing main", "only release/main", "only tag main", "unreachable origin", "unreachable cached origin", "authentication failure", "up to date", "behind from nested module"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			versionFixtureGit(t, root, "init", "--quiet", "-b", "main")
			if kind != "no origin" {
				origin := t.TempDir()
				if kind == "unreachable origin" {
					origin = filepath.Join(origin, "missing")
				} else {
					versionFixtureGit(t, origin, "init", "--quiet", "--bare")
				}
				versionFixtureGit(t, root, "remote", "add", "origin", origin)
				if kind == "only release/main" || kind == "only tag main" {
					tree := versionFixtureGit(t, origin, "hash-object", "-t", "tree", "--stdin")
					commit := versionFixtureGit(t, origin, "commit-tree", tree, "-m", "other ref")
					ref := "refs/heads/release/main"
					if kind == "only tag main" {
						ref = "refs/tags/main"
					}
					versionFixtureGit(t, origin, "update-ref", ref, commit)
				}
				if kind == "up to date" || kind == "behind from nested module" || kind == "unreachable cached origin" {
					tree := versionFixtureGit(t, origin, "hash-object", "-t", "tree", "--stdin")
					base := versionFixtureGit(t, origin, "commit-tree", tree, "-m", "base")
					versionFixtureGit(t, origin, "update-ref", "refs/heads/main", base)
					versionFixtureGit(t, root, "fetch", "--quiet", "origin", "main")
					versionFixtureGit(t, root, "update-ref", "refs/heads/main", base)
					if kind == "unreachable cached origin" {
						versionFixtureGit(t, root, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing"))
					}
					if kind == "behind from nested module" {
						next := versionFixtureGit(t, origin, "commit-tree", tree, "-p", base, "-m", "next")
						versionFixtureGit(t, origin, "update-ref", "refs/heads/main", next)
						root = filepath.Join(root, "module", "nested")
						if err := os.MkdirAll(root, 0o755); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if kind == "authentication failure" {
				realGit, err := exec.LookPath("git")
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("VERSION_FIXTURE_GIT", realGit)
				bin := t.TempDir()
				stub := "#!/bin/sh\ncase \"$*\" in *' fetch '*|*' ls-remote '*) echo 'fatal: Authentication failed' >&2; exit 128;; esac\nexec \"$VERSION_FIXTURE_GIT\" \"$@\"\n"
				if err := os.WriteFile(filepath.Join(bin, "git"), []byte(stub), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			behind, by, err := localBehindRemote(context.Background(), root)
			if kind == "unreachable origin" || kind == "unreachable cached origin" || kind == "authentication failure" {
				if err == nil {
					t.Fatal("unreachable origin produced a clean verdict")
				}
			} else if kind == "behind from nested module" {
				if err != nil || !behind || by != 1 {
					t.Fatalf("behind = %v, %d, %v, want true, 1, nil", behind, by, err)
				}
			} else if err != nil || behind || by != 0 {
				t.Fatalf("verdict = %v, %d, %v, want false, 0, nil", behind, by, err)
			}
		})
	}
}
