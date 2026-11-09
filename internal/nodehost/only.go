package nodehost

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func selectionSkips(doc nodewire.PlanDoc, options runspec.Options) (map[string]string, error) {
	if err := validateStepRange(doc, options.StartAt.Value, options.StopAt.Value); err != nil {
		return nil, err
	}
	return store.PlanSelectionSkips(doc, options.Only.Value, options.StartAt.Value, options.StopAt.Value)
}

// safety: steps outside the selection never run and need no allowance; the
// executor still checks every step it starts, including generated jobs.
func unapprovedRisk(doc nodewire.PlanDoc, options runspec.Options, jobSkips map[string]string) error {
	if options.DryRun.Value {
		return nil
	}
	var missing []string
	var firstJob, firstStep string
	for _, job := range doc.Jobs {
		if _, skipped := jobSkips[job.ID]; skipped {
			continue
		}
		children := map[string][]string{}
		for _, step := range job.Steps {
			for _, need := range step.Needs {
				children[need] = append(children[need], step.ID)
			}
		}
		stepSkips := rangeSkips(job.Steps, children, options.StartAt.Value, options.StopAt.Value)
		for _, step := range job.Steps {
			if _, skipped := stepSkips[step.ID]; skipped {
				continue
			}
			for _, risk := range step.Risks {
				if slices.Contains(options.AllowRisk.Value, risk) || slices.Contains(missing, risk) {
					continue
				}
				if firstStep == "" {
					firstJob, firstStep = job.ID, step.ID
				}
				missing = append(missing, risk)
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	return fmt.Errorf("step %q in job %q requires --sw-allow-risk %s to confirm (or --sw-dry-run to preview)", firstStep, firstJob, strings.Join(missing, ","))
}
