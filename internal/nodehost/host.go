package nodehost

import (
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

// AcceptedRun carries the immutable seal shared by planning, resume and execution.
// Spec is the canonical runspec document stored by C1; hosts cannot reinterpret
// flags, YAML, machine defaults or environment after accepting its hash.
type AcceptedRun struct {
	RunID    string
	Spec     json.RawMessage
	SpecHash string
	Plan     nodeproto.PlanDoc
	PlanHash string
}

// RunInput binds intake's sealed request to the current owner authority.
// Accepted is present on resume. A new run resolves and submits the second seal
// and plan together before any scheduling or dispatch.
type RunInput struct {
	Storage       StorageBinding
	Engines       *engine.Resolver
	GrantedRunner nodeproto.RunnerIdentity
	RunID         string
	SealedRequest json.RawMessage
	RequestHash   string
	Owner         runstate.Actor
	Accepted      *AcceptedRun
}

// RunCompletion is returned only after FinishRunAndUnit commits the verdict and
// effect intents. Publication may fail independently without changing Outcome.
type RunCompletion struct {
	Outcome string
	Reason  string
}

// RunHost owns acquire, resolve, plan, accept, decide, dispatch and expansion.
// Local, triggered, runner, here and Actions hosts use this same lifecycle.
// Controller ownership runs the same store decider with plan/eval executors.
// Engines selects the pinned release after acquisition; its identity seals with the plan.
type RunHost interface {
	Run(context.Context, RunInput) (RunCompletion, error)
}

// GeneratorReplay loads persisted source outputs only to reconstruct job code.
// Replayed declarations must match each persisted spec hash and cannot mutate
// topology, add jobs or overwrite the accepted expansion.
type GeneratorReplay struct {
	SourceJob        string
	SourceGeneration int64
	GeneratorID      string
	Output           json.RawMessage
}

// ClaimAuthority refers to the authenticated executor capability. Team, node,
// holder and generation are checked against its server-owned binding on every write.
type ClaimAuthority struct {
	CapabilityPrefix string
	Holder           string
}

// NodeInput ties an executor to one claim and the last persisted node-wide attempt ordinal.
// A new node has ordinal zero; the execution start returns its first or next ordinal.
// GrantedRunner and RouteBinding come from the award and delegated node bearer;
// SDK processes receive them in hello, never through process environment.
type NodeInput struct {
	processRevoke func() error
	Storage       StorageBinding
	Engine        engine.Resolved
	Accepted      AcceptedRun
	NodeID        string
	Execution     runspec.JobExecutionSettings
	SpecHash      string
	Generation    runspec.ClaimGeneration
	Attempt       runspec.AttemptOrdinal
	Entry         *source.Entry
	Authority     ClaimAuthority
	GrantedRunner nodeproto.RunnerIdentity
	RouteBinding  nodeproto.ProcessBinding
	Replay        []GeneratorReplay
}

// StorageBinding joins verified bytes and transactional records under one live unit authority.
// Each attempt/generation gets a fresh binding. Clients reauthorize on every operation.
// RemoteCache is present only for the explicit read-only profile opt-in.
type StorageBinding struct {
	Bytes       blob.Store
	Records     blob.RecordStore
	Authority   blob.Authority
	RemoteCache blob.RemoteCache
}

// NodeHost runs executor layers, replans for drift, executes attempts and reports
// through TransitionNode. It never decides graph readiness or emits lifecycle
// events. Application retries retain the same generation and claim.
// Engine must match Accepted's sealed identity and Entry.Engine; executable policy is checked
// on each start even when the machine cache already contains the engine.
type NodeHost interface {
	Execute(context.Context, NodeInput) error
}

// Reconciler follows an existing holder until terminal or requeued, then calls
// the store's atomic ReconcileNode again. A wait result is never a final result.
type Reconciler interface {
	ReconcileNode(context.Context, store.ReconcileRequest) (runstate.ReconciledNode, error)
}

// ExpansionStore is the owner/eval host's persistence boundary. The Tenant
// implementation is the sole writer of expansion state and generated jobs.
type ExpansionStore interface {
	AcceptExpansion(context.Context, store.ExpansionSubmission) (store.AcceptedExpansion, error)
}

var _ ExpansionStore = (*store.Tenant)(nil)

var _ Reconciler = (*store.Tenant)(nil)
