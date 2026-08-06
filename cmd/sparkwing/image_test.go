package main

import (
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
