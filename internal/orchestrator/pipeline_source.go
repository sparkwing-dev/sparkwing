package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func ResolvePipelineSource(ctx context.Context, repoDir, pipeline, explicit string) (Commit, error) {
	source, err := projectconfig.PipelineSource(repoDir, pipeline)
	if err != nil {
		return "", err
	}
	source, explicit = strings.TrimSpace(source), strings.TrimSpace(explicit)
	if source == "" {
		source = explicit
	}
	if source == "" {
		return "", nil
	}
	revision, err := ResolveRefCommit(ctx, repoDir, source, slog.Default())
	if err != nil {
		return "", fmt.Errorf("pipeline %q source %q: %w", pipeline, source, err)
	}
	if explicit != "" && explicit != source {
		override, err := ResolveRefCommit(ctx, repoDir, explicit, slog.Default())
		if err != nil {
			return "", err
		}
		if override != revision {
			return "", fmt.Errorf("--sw-pipeline-ref %q disagrees with pipeline %q source %q", explicit, pipeline, source)
		}
	}
	return revision, nil
}

func PipelineSourceEnvironment(env []string, revision string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "SPARKWING_PIPELINE_REV=") {
			out = append(out, entry)
		}
	}
	if revision != "" {
		out = append(out, "SPARKWING_PIPELINE_REV="+revision)
	}
	return out
}
