package main

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseWorkflowPublishesImmutableSignedUpdaterAssets(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../.github/workflows/release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	for _, required := range []string{
		"SHA256SUMS.sig",
		"sparkwing-*.sig",
		"SPARKWING_RELEASE_SIGNING_KEY",
		"verify-release",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow missing %q", required)
		}
	}
	if strings.Contains(workflow, "gh release upload \"$tag\" --clobber") {
		t.Error("release workflow permits signed assets to be overwritten")
	}
}
