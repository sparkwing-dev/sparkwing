package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type typedHelpArgs struct {
	Seeds int `flag:"seeds" desc:"number of seeds"`
}

type typedHelpPipe struct{ sparkwing.Base }

var typedHelpTestSequence atomic.Uint64

func typedHelpTestName(prefix string) string {
	return prefix + strconv.FormatUint(typedHelpTestSequence.Add(1), 10)
}

func (typedHelpPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ typedHelpArgs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "noop", func(_ context.Context) error { return nil })
	return nil
}

func (typedHelpPipe) ShortHelp() string { return "fuzz the model with random seeds" }
func (typedHelpPipe) Help() string      { return "Runs a randomized fuzz sweep over the model." }
func (typedHelpPipe) Examples() []sparkwing.Example {
	return []sparkwing.Example{{Comment: "run 5000 seeds", Command: "sparkwing run typed-help-fuzz --seeds 5000"}}
}

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

func TestDescribeAll_IncludesTypedArgsPipelineAfterBind(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	entrypoint := typedHelpTestName("TypedHelpEntryDescribe")
	pipelineName := typedHelpTestName("typed-help-describe-")
	sparkwing.RegisterEntrypoint[typedHelpArgs](entrypoint, func() sparkwing.Pipeline[typedHelpArgs] {
		return typedHelpPipe{}
	})
	writeTypedHelpProject(t, pipelineName, entrypoint)
	if _, err := bindProjectPipelines(); err != nil {
		t.Fatal(err)
	}

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
