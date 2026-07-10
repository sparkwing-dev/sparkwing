package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	childAwaitMaxPollInterval      = 500 * time.Millisecond
	childAwaitMinPollInterval      = 25 * time.Millisecond
	childAdmissionInspectorTimeout = 100 * time.Millisecond
)

type childPlanAdmissionStatus int

const (
	childPlanAdmissionUnknown childPlanAdmissionStatus = iota
	childPlanAdmissionQueued
	childPlanAdmissionAdmitted
)

type childPlanAdmission struct {
	Status     childPlanAdmissionStatus
	QueuedAt   time.Time
	AdmittedAt time.Time
}

func childPlanAdmissionStatusForRun(ctx context.Context, state StateBackend, concurrency ConcurrencyBackend, runID string) (childPlanAdmission, error) {
	run, err := state.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return childPlanAdmission{Status: childPlanAdmissionUnknown}, nil
		}
		return childPlanAdmission{Status: childPlanAdmissionUnknown}, err
	}
	keys, err := planConcurrencyKeys(run.PlanSnapshot)
	if err != nil {
		return childPlanAdmission{Status: childPlanAdmissionUnknown}, err
	}
	if len(keys) == 0 {
		return childPlanAdmission{Status: childPlanAdmissionAdmitted}, nil
	}
	runTerminal := run.Status == "success" || run.Status == "failed" || run.Status == "cancelled"

	admitted := 0
	queued := false
	var queuedAt time.Time
	var admittedAt time.Time
	for _, key := range keys {
		state, err := concurrency.State(ctx, key)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return childPlanAdmission{Status: childPlanAdmissionUnknown}, err
		}
		if waiterQueuedAt, ok := planAdmissionWaiterQueuedAt(state, runID); ok {
			queued = true
			if queuedAt.IsZero() || waiterQueuedAt.Before(queuedAt) {
				queuedAt = waiterQueuedAt
			}
			continue
		}
		if holderQueuedAt, holderAdmittedAt, active, ok := planAdmissionHolderTimes(state, runID); ok && (active || runTerminal) {
			admitted++
			if !holderQueuedAt.IsZero() && (queuedAt.IsZero() || holderQueuedAt.Before(queuedAt)) {
				queuedAt = holderQueuedAt
			}
			if !holderAdmittedAt.IsZero() && holderAdmittedAt.After(admittedAt) {
				admittedAt = holderAdmittedAt
			}
			continue
		}
	}
	if queued {
		return childPlanAdmission{Status: childPlanAdmissionQueued, QueuedAt: queuedAt}, nil
	}
	if admitted == len(keys) {
		if !queuedAt.IsZero() && !admittedAt.IsZero() {
			return childPlanAdmission{
				Status:     childPlanAdmissionAdmitted,
				QueuedAt:   queuedAt,
				AdmittedAt: admittedAt,
			}, nil
		}
		return childPlanAdmission{Status: childPlanAdmissionAdmitted}, nil
	}
	return childPlanAdmission{Status: childPlanAdmissionUnknown}, nil
}

func planConcurrencyKeys(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var snapshot struct {
		PlanConcurrency   *snapshotConc  `json:"plan_concurrency"`
		PlanConcurrencies []snapshotConc `json:"plan_concurrency_groups"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	keys := make([]string, 0, 1+len(snapshot.PlanConcurrencies))
	seen := map[string]bool{}
	if snapshot.PlanConcurrency != nil && snapshot.PlanConcurrency.Key != "" {
		keys = append(keys, snapshot.PlanConcurrency.Key)
		seen[snapshot.PlanConcurrency.Key] = true
	}
	for _, group := range snapshot.PlanConcurrencies {
		if group.Key == "" || seen[group.Key] {
			continue
		}
		keys = append(keys, group.Key)
		seen[group.Key] = true
	}
	return keys, nil
}

func planAdmissionWaiterQueuedAt(state *store.ConcurrencyState, runID string) (time.Time, bool) {
	for _, waiter := range state.Waiters {
		if waiter.RunID == runID && waiter.NodeID == "" {
			return waiter.ArrivedAt, true
		}
	}
	return time.Time{}, false
}

func planAdmissionHolderTimes(state *store.ConcurrencyState, runID string) (time.Time, time.Time, bool, bool) {
	now := time.Now()
	for _, holder := range state.Holders {
		if holder.RunID == runID && holder.NodeID == "" {
			active := !holder.Superseded && holder.LeaseExpiresAt.After(now)
			return holder.QueueArrivedAt, holder.ClaimedAt, active, true
		}
	}
	return time.Time{}, time.Time{}, false, false
}

func childAwaitPollInterval(ctx context.Context, timeoutPausedForAdmission bool) time.Duration {
	if timeoutPausedForAdmission {
		return childAwaitMinPollInterval
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return childAwaitMaxPollInterval
	}
	remaining := time.Until(deadline)
	if remaining <= childAwaitMinPollInterval {
		return childAwaitMinPollInterval
	}
	interval := remaining / 2
	if interval < childAwaitMinPollInterval {
		return childAwaitMinPollInterval
	}
	if interval > childAwaitMaxPollInterval {
		return childAwaitMaxPollInterval
	}
	return interval
}
