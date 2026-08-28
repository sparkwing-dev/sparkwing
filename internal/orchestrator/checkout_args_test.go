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

func captureLog(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
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
	got := checkoutInvokeArgs("deploy", map[string]string{"region": "us-east"}, slog.Default())
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
	got := checkoutInvokeArgs("deploy", nil, slog.Default())
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
	got := checkoutInvokeArgs("deploy", stored, slog.Default())
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

	got := checkoutInvokeArgs("deploy", map[string]string{"tier": "premium"}, slog.Default())
	if _, leaked := got["region"]; leaked {
		t.Fatalf("an ancestor's project config reached this run's args: %v", got)
	}
}

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

	if len(opts.PipelineYAML.Guards.Reject) != 1 {
		t.Errorf("Guards = %+v, want the defaults.guards fallback applied", opts.PipelineYAML.Guards)
	}
}

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
