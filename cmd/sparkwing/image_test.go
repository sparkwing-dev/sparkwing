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
	for _, example := range cmdImageRollout.Examples {
		if strings.Contains(example.Command, "--profile") {
			t.Errorf("image rollout example advertises unused --profile: %q", example.Command)
		}
	}
}
