package orchestrator

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// pointRuntimeAt makes root the project root the SDK runtime reports,
// the way a pipeline binary's own walk-up from its working directory
// would. Restored after the test so the repo's own config cannot leak
// into unrelated cases.
func pointRuntimeAt(t *testing.T, root string) {
	t.Helper()
	prev := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })
}

// checkoutAt writes a project checkout containing yaml and points the
// runtime at it. Returns the checkout root.
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

// captureLog returns a logger writing into buf, for asserting on
// warnings the caller must not miss.
func captureLog(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
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
	got := checkoutInvokeArgs("deploy", map[string]string{"region": "us-east"}, slog.Default())
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
	got := checkoutInvokeArgs("deploy", nil, slog.Default())
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
	pointRuntimeAt(t, t.TempDir())

	stored := map[string]string{"region": "us-east"}
	got := checkoutInvokeArgs("deploy", stored, slog.Default())
	if len(got) != 1 || got["region"] != "us-east" {
		t.Errorf("checkoutInvokeArgs = %v, want the stored args verbatim", got)
	}
}

// The resolved project root is the boundary. A checkout with a
// .sparkwing/ but no sparkwing.yaml must NOT keep climbing: the pod's
// remote-compile path unpacks its checkout under ~/.sparkwing/, so a
// walk-up would let an operator's personal ~/.sparkwing/sparkwing.yaml
// hand arguments to somebody else's pipeline.
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

	got := checkoutInvokeArgs("deploy", map[string]string{"tier": "premium"}, slog.Default())
	if _, leaked := got["region"]; leaked {
		t.Fatalf("an ancestor's project config reached this run's args: %v", got)
	}
}

// An unparseable sparkwing.yaml means this side plans with a different
// argument set than the dispatcher used -- exactly the divergence this
// path exists to close. Continuing is right; continuing silently is
// not.
func TestCheckoutInvokeArgs_MalformedConfigWarnsAndFallsBack(t *testing.T) {
	checkoutAt(t, "defaults:\n  args:\n   - this is not a map\n\tand this is a tab\n")

	var buf bytes.Buffer
	got := checkoutInvokeArgs("deploy", map[string]string{"region": "us-east"}, captureLog(&buf))
	if got["region"] != "us-east" {
		t.Errorf("fallback lost the caller's args: %v", got)
	}
	logged := buf.String()
	if !strings.Contains(logged, "project config unreadable") {
		t.Fatalf("a malformed project config was swallowed; log was:\n%s", logged)
	}
	if !strings.Contains(logged, "sparkwing.yaml") {
		t.Errorf("warning does not name the offending file:\n%s", logged)
	}
}

// applyCheckoutProjectConfig fills the fields Run() reads, not a
// pre-merged map, because guards are evaluated from opts.PipelineYAML.
// Handing over only merged args is how a path ends up silently
// ungated.
func TestApplyCheckoutProjectConfig_FillsArgsAndGuards(t *testing.T) {
	checkoutAt(t, `defaults:
  args:
    region: eu-west
  guards:
    reject:
      - profile:controller
pipelines:
  - name: deploy
    entrypoint: Deploy
    args:
      tier: premium
`)
	opts := Options{Pipeline: "deploy"}
	applyCheckoutProjectConfig(&opts, slog.Default())

	if opts.DefaultArgs["region"] != "eu-west" {
		t.Errorf("DefaultArgs = %v, want the defaults.args block", opts.DefaultArgs)
	}
	if opts.PipelineYAML == nil {
		t.Fatal("PipelineYAML is nil; Run() would skip guards entirely")
	}
	if opts.PipelineYAML.Args["tier"] != "premium" {
		t.Errorf("PipelineYAML.Args = %v, want the entry's args block", opts.PipelineYAML.Args)
	}
	// defaults.guards has to fall through to an entry that declares
	// none, or a trigger is gated differently than `sparkwing run`.
	if len(opts.PipelineYAML.Guards.Reject) != 1 {
		t.Errorf("Guards = %+v, want the defaults.guards fallback applied", opts.PipelineYAML.Guards)
	}
}

// A caller that already resolved the layers (the CLI) keeps them.
func TestApplyCheckoutProjectConfig_DoesNotOverrideCaller(t *testing.T) {
	checkoutAt(t, `defaults:
  args:
    region: eu-west
pipelines:
  - name: deploy
    entrypoint: Deploy
`)
	opts := Options{
		Pipeline:    "deploy",
		DefaultArgs: map[string]string{"region": "caller-supplied"},
	}
	applyCheckoutProjectConfig(&opts, slog.Default())
	if opts.DefaultArgs["region"] != "caller-supplied" {
		t.Errorf("DefaultArgs = %v, want the caller's own", opts.DefaultArgs)
	}
}
