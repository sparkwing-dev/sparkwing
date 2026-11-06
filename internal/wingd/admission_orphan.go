package wingd

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func (d *Daemon) cleanupAdmissionProcesses(ctx context.Context) error {
	d.mu.Lock()
	var roots []admissionTicket
	for _, ticket := range d.admissionTickets {
		if ticket.MeasurementPID != 0 {
			roots = append(roots, *ticket)
		}
	}
	d.mu.Unlock()
	var result error
	for _, root := range roots {
		if err := context.Cause(ctx); err != nil {
			return errors.Join(result, err)
		}
		birth, err := admissionProcessBirth(root.Input.RootPID)
		if err != nil && !errors.Is(err, procgroup.ErrProcessAbsent) {
			result = errors.Join(result, err)
			continue
		}
		holderAlive := err == nil && birth == root.Input.ProcessBirth
		if root.Active && holderAlive {
			continue
		}
		empty, err := admissionExecutionEmpty(root.MeasurementPID, root.MeasurementBirth)
		if err != nil {
			result = errors.Join(result, fmt.Errorf("inspect job root %d: %w", root.MeasurementPID, err))
			continue
		}
		if !empty {
			if holderAlive {
				continue
			}
			// safety: session cleanup rechecks process identity before signaling and bounds its own wait.
			//nolint:contextcheck // safety: KillSession has no caller-context variant.
			if err := killAdmissionExecution(root.MeasurementPID, root.MeasurementBirth); err != nil {
				result = errors.Join(result, fmt.Errorf("kill orphan job root %d: %w", root.MeasurementPID, err))
				continue
			}
		}
		d.mu.Lock()
		ticket := d.admissionTickets[root.Result.ID]
		if ticket != nil && ticket.MeasurementPID == root.MeasurementPID && ticket.MeasurementBirth == root.MeasurementBirth {
			ticket.MeasurementPID, ticket.MeasurementBirth = 0, ""
			if ticket.participant != nil {
				ticket.participant.measurementPID, ticket.participant.measurementBirth = 0, ""
			}
			result = errors.Join(result, d.commitAdmissionLocked())
		}
		d.mu.Unlock()
	}
	return result
}
