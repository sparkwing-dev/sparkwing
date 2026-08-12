package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func checkoutAt(t *testing.T, yaml string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sparkwing"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	prev := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })
}

// checkoutInvokeArgs is the executing side's half of the decision that
// keeps the yaml layers OUT of the run row: both must re-read them, in
// the same order, or a stored run replans differently than it planned.
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
	got := checkoutInvokeArgs("deploy", map[string]string{"region": "us-east"})
	want := map[string]string{
		"region": "us-east",    // stored beats defaults.args
		"tier":   "premium",    // entry args beat defaults.args
		"token":  "from-entry", // supplied only by the entry
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

// A pipeline the project config does not mention still gets the
// project-wide defaults, and nothing else.
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
	got := checkoutInvokeArgs("deploy", nil)
	if got["region"] != "eu-west" {
		t.Errorf("region = %q, want eu-west", got["region"])
	}
	if _, leaked := got["tier"]; leaked {
		t.Errorf("another entry's args bled in: %v", got)
	}
}

// A runner image with the pipeline baked in has no project checkout to
// read. The stored args are then the whole set, unchanged and
// un-copied -- the pre-existing behavior for that shape.
func TestCheckoutInvokeArgs_NoProjectReturnsStored(t *testing.T) {
	prev := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(t.TempDir())
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })

	stored := map[string]string{"region": "us-east"}
	got := checkoutInvokeArgs("deploy", stored)
	if len(got) != 1 || got["region"] != "us-east" {
		t.Errorf("checkoutInvokeArgs = %v, want the stored args verbatim", got)
	}
}
