package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveGitopsRepoRequiresExplicitPortableConfiguration(t *testing.T) {
	t.Setenv("SPARKWING_GITOPS_REPO", "")

	_, err := resolveGitopsRepo("")
	if err == nil {
		t.Fatal("resolveGitopsRepo succeeded without an explicit repository")
	}
	for _, want := range []string{"--gitops-repo", "SPARKWING_GITOPS_REPO"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestResolveGitopsRepoUsesSparkwingEnvironmentConfiguration(t *testing.T) {
	want := t.TempDir()
	t.Setenv("SPARKWING_GITOPS_REPO", want)

	got, err := resolveGitopsRepo("")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved repository = %q, want %q", got, want)
	}
}

func TestResolveGitopsRepoFlagOverridesEnvironment(t *testing.T) {
	explicit := t.TempDir()
	t.Setenv("SPARKWING_GITOPS_REPO", t.TempDir())

	got, err := resolveGitopsRepo(filepath.Clean(explicit))
	if err != nil {
		t.Fatal(err)
	}
	if got != explicit {
		t.Fatalf("resolved repository = %q, want explicit %q", got, explicit)
	}
}

func TestImageRolloutDiscoveryDoesNotAssumeAPrivateCheckout(t *testing.T) {
	discovery := cmdImageRollout.Description
	for _, flag := range cmdImageRollout.Flags {
		discovery += "\n" + flag.Desc
	}
	for _, privateDefault := range []string{"~/code/gitops", "author's default", "/Users/"} {
		if strings.Contains(discovery, privateDefault) {
			t.Errorf("image rollout discovery contains private default %q", privateDefault)
		}
	}
	for _, portableConfig := range []string{"--gitops-repo", "SPARKWING_GITOPS_REPO"} {
		if !strings.Contains(discovery, portableConfig) {
			t.Errorf("image rollout discovery does not name %s", portableConfig)
		}
	}
}

func TestImageRolloutDoesNotRequireUnusedProfile(t *testing.T) {
	repo := t.TempDir()
	kustomizeDir := filepath.Join(repo, "sparkwing")
	if err := os.MkdirAll(kustomizeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "images:\n  - name: registry.example/sparkwing-runner\n    newTag: old\n"
	if err := os.WriteFile(filepath.Join(kustomizeDir, "kustomization.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runImageRollout([]string{
		"--image", "sparkwing-runner",
		"--tag", "new",
		"--gitops-repo", repo,
		"--dry-run",
	}); err != nil {
		t.Fatalf("profile-independent dry run: %v", err)
	}
}

func TestImageRolloutDoesNotAdvertiseUnusedProfile(t *testing.T) {
	for _, flag := range cmdImageRollout.Flags {
		if flag.Name == "profile" {
			t.Fatal("image rollout advertises unused --profile")
		}
	}
	for _, command := range allCommands {
		for _, example := range command.Examples {
			if !strings.Contains(example.Command, "sparkwing cluster image rollout") {
				continue
			}
			if strings.Contains(example.Command, "--profile") {
				t.Errorf("%s example advertises unused rollout --profile: %q", command.Path, example.Command)
			}
		}
	}
}

func TestImageRolloutRejectsBlankValues(t *testing.T) {
	for _, tc := range []struct{ image, tag, flag string }{
		{"runner", "", "--tag"}, {"runner", " \t", "--tag"}, {"", "v1", "--image"}, {" \t", "v1", "--image"},
	} {
		t.Run(tc.flag+tc.image+tc.tag, func(t *testing.T) {
			repo := t.TempDir()
			writeRepoFile(t, filepath.Join(repo, "sparkwing", "kustomization.yaml"), "images:\n  - name: runner\n    newTag: old\n")
			err := runImageRollout([]string{"--image", tc.image, "--tag", tc.tag, "--gitops-repo", repo, "--dry-run"})
			if err == nil || !strings.Contains(err.Error(), tc.flag+" must not be blank") {
				t.Fatalf("error = %v, want blank %s rejection", err, tc.flag)
			}
		})
	}
}

func TestGitCommitAndPushPreservesUnrelatedStagedChanges(t *testing.T) {
	for _, changed := range []bool{true, false} {
		t.Run(map[bool]string{true: "rollout changed", false: "rollout unchanged"}[changed], func(t *testing.T) {
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			repo, remote := t.TempDir(), t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				out, err := runGit(repo, args...)
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(out)
			}
			git("init", "-b", "main")
			git("config", "user.name", "Rollout Test")
			git("config", "user.email", "rollout@example.invalid")
			git("config", "commit.gpgsign", "false")
			git("config", "core.hooksPath", t.TempDir())
			git("init", "--bare", remote)
			git("remote", "add", "origin", remote)
			kust := filepath.Join(repo, "sparkwing", "kustomization.yaml")
			writeRepoFile(t, kust, "images: []\n")
			writeRepoFile(t, filepath.Join(repo, "unrelated.txt"), "initial\n")
			git("add", ".")
			git("commit", "-m", "initial")
			git("push", "-u", "origin", "main")
			prior := git("rev-parse", "HEAD")
			writeRepoFile(t, filepath.Join(repo, "unrelated.txt"), "operator staged change\n")
			git("add", "unrelated.txt")
			if changed {
				writeRepoFile(t, kust, "images: [{name: runner, newTag: v2}]\n")
			}
			_, committed, err := gitCommitAndPush(repo, kust, "chore: bump runner")
			if err != nil {
				t.Fatal(err)
			}
			if committed != changed {
				t.Errorf("committed = %v, want %v", committed, changed)
			}
			if got := git("show", "HEAD:unrelated.txt"); got != "initial" {
				t.Errorf("rollout committed unrelated content: %q", got)
			}
			if got := git("diff", "--cached", "--name-only"); got != "unrelated.txt" {
				t.Errorf("remaining staged paths = %q", got)
			}
			if changed {
				if got := git("diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"); got != "sparkwing/kustomization.yaml" {
					t.Errorf("rollout changed paths = %q", got)
				}
			} else if got := git("rev-parse", "HEAD"); got != prior {
				t.Errorf("unchanged rollout made a commit: %s", got)
			}
		})
	}
}
