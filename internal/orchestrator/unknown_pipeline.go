package orchestrator

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func unknownPipelineErr(pipeline string) error {
	return fmt.Errorf("%s", unknownPipelineMessage(pipeline))
}

func unknownPipelineMessage(pipeline string) string {
	registered := sparkwing.Registered()
	suggestion := sparkwingruntime.SuggestClosest(pipeline, registered)
	var b strings.Builder
	fmt.Fprintf(&b, "unknown pipeline %q", pipeline)
	if suggestion != "" {
		fmt.Fprintf(&b, "; did you mean %q?", suggestion)
	}
	return b.String()
}
