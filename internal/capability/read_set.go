package capability

import (
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// ReadsJobOutput reports the declared same-run output edge between two jobs.
func ReadsJobOutput(reader, output nodeproto.JobSpec) bool {
	if output.ID == "" {
		return false
	}
	if reader.OnFailureOf == output.ID || slices.Contains(reader.Needs, output.ID) || slices.Contains(reader.NeedsIfPresent, output.ID) {
		return true
	}
	for _, group := range reader.NeedsGenerated {
		if slices.Contains(output.Groups, group) {
			return true
		}
	}
	return false
}
