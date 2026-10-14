package store

import (
	"context"
	"time"
)

// RunState is both the persisted status and the logical state; terminal states never reopen.
type RunState string

// Run states match the final schema CHECK.
const (
	RunPending   RunState = "pending"
	RunRunning   RunState = "running"
	RunSuccess   RunState = "success"
	RunFailed    RunState = "failed"
	RunCancelled RunState = "cancelled"
)

// RunCause distinguishes replay-safe lifecycle writes from trigger recovery that keeps the same run alive.
type RunCause string

// Run causes let the store select a lifecycle event and completion desired state.
const (
	RunStart        RunCause = "start"
	RunFinish       RunCause = "finish"
	RunCancel       RunCause = "cancel"
	RunQueueTimeout RunCause = "queue_timeout"
	RunOwnerLost    RunCause = "owner_lost"
	RunPublish      RunCause = "publish"
)

// RunEdge records all legal run-state families; a cancel of live work is initially request-only.
type RunEdge struct {
	From         []RunState
	To           []RunState
	Actors       []ActorKind
	Cause        RunCause
	Precondition string
	RequestOnly  bool
}

// RunTransitions keeps publication, owner recovery and cooperative cancellation visible to implementers.
func RunTransitions() []RunEdge {
	active := []RunState{RunPending, RunRunning}
	terminal := []RunState{RunSuccess, RunFailed, RunCancelled}
	owners := []ActorKind{ActorRunHost, ActorController}
	return []RunEdge{
		{[]RunState{RunPending}, []RunState{RunRunning}, owners, RunStart, "live owner; accepted plan", false},
		{active, terminal, owners, RunFinish, "live owner; no live work or expected expansions", false},
		{active, []RunState{RunCancelled}, []ActorKind{ActorOperator, ActorController, ActorReaper}, RunCancel, "cancel authority; resolves expected expansions; live work drains", true},
		{active, []RunState{RunCancelled}, []ActorKind{ActorController, ActorReaper, ActorRunHost}, RunFinish, "cancel requested; no live work or expected expansions", false},
		{[]RunState{RunPending}, []RunState{RunFailed}, []ActorKind{ActorReaper}, RunQueueTimeout, "queue deadline elapsed; no live claim", false},
		{active, []RunState{RunFailed, RunCancelled}, []ActorKind{ActorReaper}, RunOwnerLost, "expired local owner or remote disruption cap; cancellation wins; no live work", false},
		{active, terminal, []ActorKind{ActorOperator}, RunPublish, "publish intake; authenticated run-bound publisher; results and bytes validated", false},
	}
}

// RunTransition never accepts a client clock or effect payload; those derive from committed state.
type RunTransition struct {
	RunID       string
	To          RunState
	Actor       Actor
	Cause       RunCause
	Reason      string
	MaskedError string
}

// FinishRunRequest binds the terminal verdict to the owner unit that must end in the same transaction.
type FinishRunRequest struct {
	Transition RunTransition
	Unit       OwnerLeaseRef
}

// TransitionRun applies the run edge, its event and revisioned completion intents together.
func (t *Tenant) TransitionRun(ctx context.Context, change RunTransition) (Applied, error) {
	panic("unimplemented: c1-data")
}

// FinishRunAndUnit prevents a terminal run from leaving an executable owner claim behind.
func (t *Tenant) FinishRunAndUnit(ctx context.Context, finish FinishRunRequest) (Applied, error) {
	panic("unimplemented: c1-data")
}

// safety: all run status updates use this path so events and desired effect revisions cannot drift.
func transitionRunTx(ctx context.Context, tx *storeTx, team Team, change RunTransition, now time.Time) (Applied, error) {
	panic("unimplemented: c1-data")
}
