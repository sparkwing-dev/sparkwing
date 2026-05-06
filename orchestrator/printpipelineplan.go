package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// printPipelineRuntimePlan implements `--plan` for the pipeline
// binary. Builds the Plan via Registration.Invoke (so IMP-008's
// Plan-time validation runs) then walks it via PreviewPlan to
// resolve per-step would-run / would-skip decisions against the
// supplied args + SPARKWING_START_AT / SPARKWING_STOP_AT env. No
// step bodies execute. The JSON shape emitted here is consumed by
// `cmd/sparkwing pipeline plan`'s wrapper (mirroring the
// --explain / pipeline_explain.go contract). IMP-013.
func printPipelineRuntimePlan(pipeline string, rest []string) error {
	reg, ok := sparkwing.Lookup(pipeline)
	if !ok {
		return fmt.Errorf("unknown pipeline %q", pipeline)
	}
	rest = stripExplainOutputFlags(rest)
	argsMap, err := parseTypedFlags(pipeline, rest)
	if err != nil {
		// Match printPipelinePlan's leniency: empty argsMap on parse
		// failure so the user sees the structural plan even when
		// flag values are off. The wrapper reports the parse error
		// in the JSON summary if useful.
		argsMap = map[string]string{}
	}
	rc := sparkwing.RunContext{
		Pipeline: pipeline,
		RunID:    "plan",
	}
	plan, err := reg.Invoke(context.Background(), argsMap, rc)
	if err != nil {
		return fmt.Errorf("build plan: %w", err)
	}

	preview, err := sparkwing.PreviewPlan(plan, pipeline, argsMap, sparkwing.PreviewOptions{
		StartAt: os.Getenv("SPARKWING_START_AT"),
		StopAt:  os.Getenv("SPARKWING_STOP_AT"),
		DryRun:  os.Getenv("SPARKWING_DRY_RUN") == "1",
	})
	if err != nil {
		return fmt.Errorf("preview plan: %w", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(preview); err != nil {
		return fmt.Errorf("encode preview: %w", err)
	}
	return nil
}
