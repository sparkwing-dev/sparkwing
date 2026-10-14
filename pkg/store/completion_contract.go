package store

import (
	"context"
	"encoding/json"
	"time"
)

// Effect identifies one revisioned desired-state row per team and run.
type Effect string

// Completion effects replace every finish-handler side effect.
const (
	EffectGitHubCheck  Effect = "github_check"
	EffectMetrics      Effect = "metrics"
	EffectProfileFold  Effect = "profile_fold"
	EffectNotifyParent Effect = "notify_parent"
)

// EffectPhase is ranked independently of the terminal outcome so a late pending write cannot regress it.
type EffectPhase string

// Effect phases only advance; terminal outcome changes are illegal run transitions.
const (
	EffectPending  EffectPhase = "pending"
	EffectRunning  EffectPhase = "running"
	EffectTerminal EffectPhase = "terminal"
)

// EffectPolicy controls delivery phases and external idempotency keys.
type EffectPolicy struct {
	Effect       Effect
	TerminalOnly bool
	Idempotency  string
}

// CompletionEffects is the entire effect registry consumed by the elected reconciler.
func CompletionEffects() []EffectPolicy {
	return []EffectPolicy{
		{EffectGitHubCheck, false, "team/run/effect/revision; serialized report lock"},
		{EffectMetrics, true, "team/run/effect"},
		{EffectProfileFold, true, "team/run/effect"},
		{EffectNotifyParent, true, "team/run/effect"},
	}
}

// EffectPhaseRanks supplies the monotone desired-state order.
func EffectPhaseRanks() map[EffectPhase]int {
	return map[EffectPhase]int{EffectPending: 0, EffectRunning: 1, EffectTerminal: 2}
}

// DesiredRunEffect is a committed snapshot for delivery; Payload comes from the run, never an executor.
type DesiredRunEffect struct {
	Team         Team
	RunID        string
	Effect       Effect
	Phase        EffectPhase
	State        RunState
	Payload      json.RawMessage
	DesiredRev   int64
	DeliveredRev int64
	Attempts     int64
	NextAt       time.Time
}

// EffectDelivery acknowledges only the revision actually sent, leaving a newer desired revision due.
type EffectDelivery struct {
	RunID    string
	Effect   Effect
	Revision int64
	Lease    SingletonLease
}

// DueRunEffects reads revisions under the completion singleton lease; expiry stops further delivery acknowledgements.
func (o *Operator) DueRunEffects(ctx context.Context, lease SingletonLease, limit int) ([]DesiredRunEffect, error) {
	panic("unimplemented: c1-data")
}

// AcknowledgeRunEffect advances delivered_rev only to the delivered revision, never to a newly observed desired_rev.
func (t *Tenant) AcknowledgeRunEffect(ctx context.Context, delivery EffectDelivery) error {
	panic("unimplemented: c1-data")
}

// RetryRunEffect records a failed delivery without overwriting a later desired state or its due work.
func (t *Tenant) RetryRunEffect(ctx context.Context, delivery EffectDelivery, nextAt time.Time) error {
	panic("unimplemented: c1-data")
}
