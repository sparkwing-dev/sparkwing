package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func pointRuntimeAt(t *testing.T, root string) {
	t.Helper()
	prev := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })
}

func checkoutAt(t *testing.T, yaml string) string {
	t.Helper()
	root := t.TempDir()
	writeProjectConfig(t, root, yaml)
	pointRuntimeAt(t, root)
	return root
}

func writeProjectConfig(t *testing.T, root, yaml string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".sparkwing"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestCheckoutInvokeArgs_LayersYAMLUnderStored(t *testing.T) {
	checkoutAt(t, `defaults:
  args:
    region: eu-west
    tier: standard
pipelines:
  - name: deploy
    entrypoint: Deploy
    args:
      tier: premium
      token: from-entry
`)
	got, err := checkoutInvokeArgs("deploy", map[string]string{"region": "us-east"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"region": "us-east",
		"tier":   "premium",
		"token":  "from-entry",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (full: %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("merged args = %v, want exactly %v", got, want)
	}
}

func TestCheckoutInvokeArgs_UnlistedPipelineGetsDefaultsOnly(t *testing.T) {
	checkoutAt(t, `defaults:
  args:
    region: eu-west
pipelines:
  - name: other
    entrypoint: Other
    args:
      tier: premium
`)
	got, err := checkoutInvokeArgs("deploy", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["region"] != "eu-west" {
		t.Errorf("region = %q, want eu-west", got["region"])
	}
	if _, leaked := got["tier"]; leaked {
		t.Errorf("another entry's args bled in: %v", got)
	}
}

func TestCheckoutInvokeArgs_NoProjectReturnsStored(t *testing.T) {
	pointRuntimeAt(t, t.TempDir())

	stored := map[string]string{"region": "us-east"}
	got, err := checkoutInvokeArgs("deploy", stored)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["region"] != "us-east" {
		t.Errorf("checkoutInvokeArgs = %v, want the stored args verbatim", got)
	}
}

func TestCheckoutInvokeArgs_AncestorConfigIsNotMerged(t *testing.T) {
	ancestor := t.TempDir()
	writeProjectConfig(t, ancestor, `defaults:
  args:
    region: someone-elses-machine
`)
	child := filepath.Join(ancestor, "fetched", "checkout")
	if err := os.MkdirAll(filepath.Join(child, ".sparkwing"), 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	pointRuntimeAt(t, child)

	got, err := checkoutInvokeArgs("deploy", map[string]string{"tier": "premium"})
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := got["region"]; leaked {
		t.Fatalf("an ancestor's project config reached this run's args: %v", got)
	}
}

// A key a release removed makes the checkout's config unreadable, and the run
// fails on it instead of planning without the project's defaults and guards.
func TestCheckoutConfig_ARemovedKeyFailsTheRun(t *testing.T) {
	checkoutAt(t, "profiles:\n  prod:\n    mirror_local: false\npipelines:\n  - name: deploy\n    entrypoint: Deploy\n")
	if _, err := checkoutInvokeArgs("deploy", nil); err == nil || !strings.Contains(err.Error(), "field mirror_local not found") {
		t.Fatalf("checkoutInvokeArgs = %v, want the unknown-field error", err)
	}
}
