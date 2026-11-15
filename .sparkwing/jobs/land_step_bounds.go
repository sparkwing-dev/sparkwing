package jobs

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: the land job's first step; stopping there runs every check and
// skips the push.
const landCheckOnlyStop = "verify"

// safety: step bounds narrow only the job that holds them. The land job holds
// neither bound, so it would still push a tree whose skipped checks never ran.
func refuseLandStepBounds(rc sparkwing.RunContext) error {
	if rc.DryRun || (rc.StartAt == "" && rc.StopAt == "") || rc.StopAt == landCheckOnlyStop {
		return nil
	}
	var flags []string
	if rc.StartAt != "" {
		flags = append(flags, "--sw-start-at "+rc.StartAt)
	}
	if rc.StopAt != "" {
		flags = append(flags, "--sw-stop-at "+rc.StopAt)
	}
	var skipped string
	switch {
	case rc.StartAt != "" && rc.StopAt != "":
		skipped = fmt.Sprintf("every check outside %s..%s", rc.StartAt, rc.StopAt)
	case rc.StartAt != "":
		skipped = "every check upstream of " + rc.StartAt
	default:
		skipped = "every check downstream of " + rc.StopAt
	}
	return fmt.Errorf("land: %s skips %s in its job, yet the land job would still push; a pushing Land runs every check. Run every check without pushing with --sw-stop-at %s, or preview with --sw-dry-run",
		strings.Join(flags, " "), skipped, landCheckOnlyStop)
}
