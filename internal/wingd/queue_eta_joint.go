package wingd

import (
	"math"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func simulateAdmissionETA(qs *wingwire.QueueState, snap admission.Snapshot) ([]float64, float64) {
	durations := make(map[string]float64, len(snap.Leases)+len(snap.Waiters))
	rows := semaphoreETAHolderRows(qs, snap)
	for _, lease := range snap.Leases {
		row := rows[lease.ID]
		durations[lease.RequestID] = remainingMS(row.ExpectedDurationMS, row.ElapsedMS)
	}
	for i, waiter := range snap.Waiters {
		durations[waiter.RequestID] = durationMS(qs.Waiters[i].ExpectedDurationMS)
	}
	predicted, clear, err := admission.Predict(snap, durations)
	if err != nil {
		clear = math.Inf(1)
	}
	starts := make([]float64, len(snap.Waiters))
	for i, waiter := range snap.Waiters {
		starts[i] = predicted[waiter.RequestID]
	}
	return starts, clear
}

func finiteETA(v float64) (int64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= float64(math.MaxInt64) {
		return 0, false
	}
	ms := int64(v)
	return ms, ms >= 0
}
