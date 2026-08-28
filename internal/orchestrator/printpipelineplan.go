package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func printPipelineRuntimePlan(pipeline string, rest []string) error {
	reg, ok := sparkwing.Lookup(pipeline)
	if !ok {
		return unknownPipelineErr(pipeline)
	}
	rest = stripExplainOutputFlags(rest)
	argsMap, err := parseTypedFlags(pipeline, rest)
	if err != nil {
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

	preview, err := sparkwingruntime.PreviewPlan(plan, pipeline, argsMap, sparkwingruntime.PreviewOptions{
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
