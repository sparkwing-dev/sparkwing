package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	return childPlanAdmissionScan(ctx, state, concurrency, runID, false)
}

func childPlanAdmissionStatusForGlobalKeys(ctx context.Context, state StateBackend, concurrency ConcurrencyBackend, runID string) (childPlanAdmission, error) {
	return childPlanAdmissionScan(ctx, state, concurrency, runID, true)
}

func childPlanAdmissionScan(ctx context.Context, state StateBackend, concurrency ConcurrencyBackend, runID string, globalOnly bool) (childPlanAdmission, error) {
	run, err := state.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return childPlanAdmission{Status: childPlanAdmissionUnknown}, nil
		}
		return childPlanAdmission{Status: childPlanAdmissionUnknown}, err
	}
	// safety: a controller-dispatched child has an empty plan until its plan
	// is accepted, which would read as admitted with no keys; until then it
	// is queued, so the parent's timeout stays paused while it plans.
	if run.Admission == store.RunAdmissionPlanning {
		if run.Status != "pending" && run.Status != "running" {
			return childPlanAdmission{Status: childPlanAdmissionUnknown}, nil
		}
		return childPlanAdmission{Status: childPlanAdmissionQueued, QueuedAt: run.CreatedAt}, nil
	}
	keys, err := planConcurrencyKeys(run.PlanSnapshot, runID)
	if err != nil {
		return childPlanAdmission{Status: childPlanAdmissionUnknown}, err
	}
	if globalOnly {
		keys = filterGlobalScopeKeys(keys)
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

func planConcurrencyKeys(raw []byte, runID string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var doc nodeproto.PlanDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var keys []string
	seen := map[string]bool{}
	for _, group := range doc.Concurrency {
		key := nodehost.ConcurrencyKey(group, runID, boxHostID())
		if !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	return keys, nil
}

func filterGlobalScopeKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if scope, _, _, ok := nodehost.ParseConcurrencyKey(key); ok && scope == "global" {
			out = append(out, key)
		}
	}
	return out
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
