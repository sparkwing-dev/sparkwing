package opsview

import (
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// AdmissionWaitLine explains machine accounting and the waiter's place in its queue.
func AdmissionWaitLine(state wingwire.QueueState, waiter wingwire.Waiter) string {
	var holders []string
	for _, holder := range queueLifecycleRows(state, false) {
		name := holder.Pipeline
		if name == "" {
			name = queueDisplayRunID(holder.RunID, holder.DisplayRunID)
		}
		holders = append(holders, name+" "+fmtCost(holder.Resources))
	}
	line := fmt.Sprintf("admission: %d running", len(holders))
	if len(holders) > 0 {
		line += " (" + strings.Join(holders, ", ") + ")"
	}
	line += fmt.Sprintf(", %d queued; ", len(state.Waiters))
	if waiter.Position <= 1 {
		line += "you are next"
	} else {
		line += fmt.Sprintf("position %d of %d", waiter.Position, len(state.Waiters))
	}
	for _, resource := range state.Resources {
		need := float64(0)
		switch resource.Key {
		case "cores":
			need = waiter.Resources.Cores
		case "memory":
			need = float64(waiter.Resources.MemoryBytes)
		}
		if need <= 0 {
			continue
		}
		line += fmt.Sprintf(" - needs %s %s", fmtAmount(resource.Key, need), resource.Key)
		rationale := waiter.CostRationale
		if rationale == "" {
			rationale = wingwire.CostRationale(wingwire.CostSource(waiter.CostSource), 0)
		}
		if rationale != "" {
			line += " (" + rationale + ")"
		}
		line += fmt.Sprintf(", %s free (capacity %s, held %s, reserved %s, external %s)",
			fmtAmount(resource.Key, resource.Available), fmtAmount(resource.Key, resource.Capacity),
			fmtAmount(resource.Key, resource.Held), fmtAmount(resource.Key, resource.Reserved), externalAmount(resource))
	}
	if state.IgnoreExternal {
		line += "; external ignored"
	}
	if len(waiter.WaitingOn) > 0 {
		line += "; waiting on " + strings.Join(waiter.WaitingOn, ", ")
	}
	if state.ExpectedClearMS != nil {
		line += "; expected clear ~" + (time.Duration(*state.ExpectedClearMS) * time.Millisecond).Round(time.Second).String()
	}
	return line
}
