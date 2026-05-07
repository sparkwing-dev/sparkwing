package orchestrator

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// unknownPipelineErr formats the canonical "unknown pipeline X"
// error with a "did you mean Y?" Levenshtein suggestion sourced from
// the live registration set. Reuses sparkwing.SuggestClosest (the
// IMP-008 helper) so the typo threshold matches the rest of the
// SDK's string-id surfaces.
//
// IMP-040: previously every "unknown pipeline" site returned a flat
// error and forced the operator to re-read `sparkwing pipeline list`
// for the right spelling. Now `wing claster-up` suggests
// "cluster-up" inline, mirroring IMP-008's `Needs("X")` typo
// suggestion. Far typos ("totallyunrelated") fall through to the
// existing message without a misleading suggestion.
func unknownPipelineErr(pipeline string) error {
	return fmt.Errorf("%s", unknownPipelineMessage(pipeline))
}

// unknownPipelineMessage is the string body of unknownPipelineErr,
// exposed separately so cmd/sparkwing's `pipeline describe` (whose
// error string is shaped slightly differently -- "no pipeline named
// X" rather than "unknown pipeline X") can reuse the suggestion
// logic without inheriting our message prefix.
func unknownPipelineMessage(pipeline string) string {
	registered := sparkwing.Registered()
	suggestion := sparkwing.SuggestClosest(pipeline, registered)
	var b strings.Builder
	fmt.Fprintf(&b, "unknown pipeline %q", pipeline)
	if suggestion != "" {
		fmt.Fprintf(&b, "; did you mean %q?", suggestion)
	}
	return b.String()
}
