package nodehost

import (
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	RunID         string
	SealedRequest json.RawMessage
	RequestHash   string
	Owner         store.ReconcileActor
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

// NodeInput ties an executor to one claim and node-wide attempt ordinal.
// GrantedRunner and RouteBinding come from the award and delegated node bearer;
// SDK processes receive them in hello, never through process environment.
type NodeInput struct {
	Accepted      AcceptedRun
	NodeID        string
	SpecHash      string
	Generation    int64
	Attempt       int64
	Entry         *source.Entry
	Authority     ClaimAuthority
	GrantedRunner nodeproto.RunnerIdentity
	RouteBinding  nodeproto.ProcessBinding
	Replay        []GeneratorReplay
}

// NodeHost runs executor layers, replans for drift, executes attempts and reports
// through TransitionNode. It never decides graph readiness or emits lifecycle
// events. Application retries retain the same generation and claim.
type NodeHost interface {
	Execute(context.Context, NodeInput) error
}

// LayerSide states who decides a layer even if a pod evaluates its closure.
type LayerSide string

const (
	OwnerSide    LayerSide = "owner"
	ExecutorSide LayerSide = "executor"
)

// LayerSpec records outer-to-inner order and the boundary around application retries.
// Owner layers run once for a scheduling pass; attempt timeout runs on every try.
type LayerSpec struct {
	Name       string
	Side       LayerSide
	PerAttempt bool
	Contract   string
}

// LayerTable is the complete envelope. Hosts select by side without maintaining
// placement-specific lists. The controller records owner decisions through the
// same transitions when plan or work pods evaluate their closures.
func LayerTable() []LayerSpec {
	return []LayerSpec{
		{"debug", OwnerSide, false, "pause at configured boundaries; cancellation and leases remain live"},
		{"skip_if", OwnerSide, false, "evaluate declared predicates within skip budget before approval or compute"},
		{"approval", OwnerSide, false, "wait under owner liveness; zero timeout is indefinite where the owner can wait"},
		{"memo", OwnerSide, false, "authorize recorded output and artifact edges; reconciliation commits the hit"},
		{"admission", OwnerSide, false, "hold run or node capacity under store liveness and continuous-adoption rules"},
		{"artifacts", ExecutorSide, false, "stage declared inputs before work; publish only verified, authorized references"},
		{"caches", ExecutorSide, false, "restore depcache before work and save under authorized repo/ref scope"},
		{"retry", ExecutorSide, false, "retry application failures under the same claim; ordinal increases; disruptions are separate"},
		{"timeout", ExecutorSide, true, "derive attempt and no-progress deadlines from limits and any owner deadline"},
	}
}

// Reconciler follows an existing holder until terminal or requeued, then calls
// the store's atomic ReconcileNodeC3 again. A wait result is never a final result.
type Reconciler interface {
	Reconcile(context.Context, store.ReconcileRef, store.ReconcileActor, store.SourcePolicyC3) (store.ReconcileResult, error)
}

// ExpansionStore is the owner/eval host's persistence boundary. The Tenant
// implementation is the sole writer of expansion state and generated jobs.
type ExpansionStore interface {
	AcceptExpansionC3(context.Context, store.ExpansionAuthority, store.ExpansionSubmissionC3) (store.ExpansionResult, error)
	FailExpansion(context.Context, store.ExpansionAuthority, store.ExpansionSubmissionC3, string) error
}

var _ ExpansionStore = (*store.Tenant)(nil)
