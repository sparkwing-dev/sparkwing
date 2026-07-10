package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type typedHelpArgs struct {
	Seeds int `flag:"seeds" desc:"number of seeds"`
}

type typedHelpPipe struct{ sparkwing.Base }

func (typedHelpPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ typedHelpArgs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "noop", func(_ context.Context) error { return nil })
	return nil
}

func (typedHelpPipe) ShortHelp() string { return "fuzz the model with random seeds" }
func (typedHelpPipe) Help() string      { return "Runs a randomized fuzz sweep over the model." }
func (typedHelpPipe) Examples() []sparkwing.Example {
	return []sparkwing.Example{{Comment: "run 5000 seeds", Command: "sparkwing run typed-help-fuzz --seeds 5000"}}
}

// writeTypedHelpProject stages a .sparkwing/sparkwing.yaml under a temp
// dir binding pipeline name -> entrypoint, chdirs into it, and restores
// the cwd on cleanup.
func writeTypedHelpProject(t *testing.T, pipelineName, entrypoint string) {
	t.Helper()
	dir := t.TempDir()
	sparkwingDir := filepath.Join(dir, ".sparkwing")
	if err := os.MkdirAll(sparkwingDir, 0o755); err != nil {
		t.Fatalf("mkdir .sparkwing: %v", err)
	}
	yaml := "pipelines:\n  - name: " + pipelineName + "\n    entrypoint: " + entrypoint + "\n"
	if err := os.WriteFile(filepath.Join(sparkwingDir, "sparkwing.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write sparkwing.yaml: %v", err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func TestBindProjectPipelines_ResolvesTypedArgsPipelineForHelp(t *testing.T) {
	const entrypoint = "TypedHelpEntry"
	const pipelineName = "typed-help-fuzz"
	sparkwing.RegisterEntrypoint[typedHelpArgs](entrypoint, func() sparkwing.Pipeline[typedHelpArgs] {
		return typedHelpPipe{}
	})

	if _, ok := sparkwing.Lookup(pipelineName); ok {
		t.Fatalf("precondition: %q already bound before test staged its config", pipelineName)
	}
	if err := printPipelineHelp(pipelineName); err == nil {
		t.Fatal("printPipelineHelp should fail with unknown pipeline before the YAML bind")
	}

	writeTypedHelpProject(t, pipelineName, entrypoint)

	cfg := bindProjectPipelines()
	if cfg == nil {
		t.Fatal("bindProjectPipelines returned nil config for a valid project")
	}

	reg, ok := sparkwing.Lookup(pipelineName)
	if !ok {
		t.Fatalf("Lookup(%q) failed after bindProjectPipelines", pipelineName)
	}
	if reg.Name != pipelineName {
		t.Errorf("reg.Name = %q, want %q", reg.Name, pipelineName)
	}

	dp, ok, err := sparkwingruntime.DescribePipelineByName(pipelineName)
	if err != nil {
		t.Fatalf("DescribePipelineByName: %v", err)
	}
	if !ok {
		t.Fatalf("DescribePipelineByName(%q) not found after bind", pipelineName)
	}
	if dp.Short != "fuzz the model with random seeds" {
		t.Errorf("Short = %q, want the ShortHelp text", dp.Short)
	}
	if !hasArg(dp.Args, "seeds") {
		t.Errorf("Args = %+v, want a --seeds flag", dp.Args)
	}

	if err := printPipelineHelp(pipelineName); err != nil {
		t.Errorf("printPipelineHelp after bind: %v", err)
	}
}

func TestDescribeAll_IncludesTypedArgsPipelineAfterBind(t *testing.T) {
	const entrypoint = "TypedHelpEntryDescribe"
	const pipelineName = "typed-help-describe"
	sparkwing.RegisterEntrypoint[typedHelpArgs](entrypoint, func() sparkwing.Pipeline[typedHelpArgs] {
		return typedHelpPipe{}
	})
	writeTypedHelpProject(t, pipelineName, entrypoint)
	bindProjectPipelines()

	all, err := sparkwingruntime.DescribeAll()
	if err != nil {
		t.Fatalf("DescribeAll: %v", err)
	}
	for _, dp := range all {
		if dp.Name == pipelineName {
			if dp.Short == "" {
				t.Errorf("DescribeAll entry %q has empty Short", pipelineName)
			}
			return
		}
	}
	t.Errorf("DescribeAll did not include YAML-bound typed-Args pipeline %q", pipelineName)
}

func hasArg(args []sparkwing.DescribeArg, name string) bool {
	for _, a := range args {
		if a.Name == name {
			return true
		}
	}
	return false
}
