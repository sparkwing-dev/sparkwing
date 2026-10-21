package localhostwire

import (
	"fmt"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// LocalDataAuthority carries the controller's canonical live storage projection.
type LocalDataAuthority struct {
	Authority blob.Authority `json:"authority"`
}

// LocalNodeSnapshot keeps artifact selectors outside the executable job namespace.
type LocalNodeSnapshot struct {
	store.RunSnapshot
	ArtifactSources map[string]blob.RecordKey `json:"artifact_sources,omitempty"`
}

// LocalRunTransition carries a lifecycle edge without actor authority.
type LocalRunTransition struct {
	To          store.RunState           `json:"to"`
	Cause       store.RunCause           `json:"cause"`
	Reason      string                   `json:"reason"`
	MaskedError string                   `json:"masked_error"`
	OwnerPolicy *store.OwnerPolicyResult `json:"owner_policy,omitempty"`
}

// LocalRunCompletion requests the store-derived verdict without choosing an outcome.
type LocalRunCompletion struct {
	Cause       store.RunCause           `json:"cause"`
	Reason      string                   `json:"reason"`
	MaskedError string                   `json:"masked_error"`
	OwnerPolicy *store.OwnerPolicyResult `json:"owner_policy,omitempty"`
}

// LocalNodeTransition carries an edge and its captured source attempt.
type LocalNodeTransition struct {
	To            store.NodeState         `json:"to"`
	Cause         store.TransitionCause   `json:"cause"`
	SourceAttempt *runspec.AttemptOrdinal `json:"source_attempt,omitempty"`
	Extra         LocalTransitionExtra    `json:"extra"`
}

// LocalTransitionExtra omits award and execution authority.
type LocalTransitionExtra struct {
	OwnerPolicy *store.OwnerPolicyResult  `json:"owner_policy,omitempty"`
	Result      *store.NodeResult         `json:"result,omitempty"`
	Materialize *store.MaterializeSource  `json:"materialize,omitempty"`
	Approval    *store.ApprovalResolution `json:"approval,omitempty"`
	RetryAt     *time.Time                `json:"retry_at,omitempty"`
}

// LocalAuthenticationRequest selects an authority within the host's awarded run.
type LocalAuthenticationRequest struct {
	Lookup   capability.CredentialLookup `json:"lookup"`
	Evidence capability.RequestEvidence  `json:"evidence"`
}

// LocalAuthentication preserves digest fields omitted by public capability JSON.
type LocalAuthentication struct {
	Snapshot capability.AuthenticationSnapshot `json:"snapshot"`
	Digests  [][32]byte                        `json:"digests"`
}

// LocalBindingRequest compares a selected authority with fresh persisted relations.
type LocalBindingRequest struct {
	Authority capability.AuthorityRef `json:"authority"`
	Rule      capability.BindingRule  `json:"rule"`
	Target    capability.Target       `json:"target"`
}

// LocalBindingChecked acknowledges a successful persisted binding check.
type LocalBindingChecked struct {
	Checked bool `json:"checked"`
}

// NodeCommand constructs the named operation admitted by the local transport.
func (body LocalNodeTransition) NodeCommand(ref controllerwire.NodeRef, actor store.Actor) (store.NodeCommand, error) {
	var command store.NodeCommand
	switch body.Cause {
	case store.CauseStart:
		command = store.StartNode{Ref: ref, Actor: actor, SourceAttempt: body.SourceAttempt}
	case store.CauseRetry:
		command = store.RetryNode{Ref: ref, Actor: actor, SourceAttempt: body.SourceAttempt, Result: body.Extra.Result, RetryAt: body.Extra.RetryAt}
	case store.CauseReport:
		command = store.ReportNode{Ref: ref, Actor: actor, SourceAttempt: body.SourceAttempt, Outcome: body.To, Result: body.Extra.Result}
	case store.CauseRelease:
		if body.To == store.NodeReady {
			command = store.ReadyNode{Ref: ref, Actor: actor}
		} else {
			command = store.ReleaseNode{Ref: ref, Actor: actor, SourceAttempt: body.SourceAttempt}
		}
	case store.CauseCancel:
		command = store.CancelNode{Ref: ref, Actor: actor, Result: body.Extra.Result}
	case store.CauseDependency:
		command = store.DependencyNode{Ref: ref, Actor: actor, Outcome: body.To, Result: body.Extra.Result}
	case store.CauseOwnerPolicy:
		command = store.OwnerPolicyNode{Ref: ref, Actor: actor, Outcome: body.To, Result: body.Extra.Result, OwnerPolicy: body.Extra.OwnerPolicy}
	case store.CauseApprovalOpen:
		command = store.OpenApprovalNode{Ref: ref, Actor: actor}
	case store.CauseApprovalResolve:
		command = store.ResolveApprovalNode{Ref: ref, Actor: actor, Outcome: body.To, Approval: body.Extra.Approval}
	case store.CauseApprovalTimeout:
		command = store.TimeoutApprovalNode{Ref: ref, Actor: actor}
	case store.CauseMaterialize:
		command = store.MaterializeNode{Ref: ref, Actor: actor, Materialize: body.Extra.Materialize}
	default:
		return nil, fmt.Errorf("unsupported local node command %q", body.Cause)
	}
	encoded, err := LocalNodeCommand(command)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(body, encoded) {
		return nil, fmt.Errorf("node command carries conflicting fields")
	}
	return command, nil
}

// LocalNodeCommand strips actor authority from a named command before transmission.
func LocalNodeCommand(command store.NodeCommand) (LocalNodeTransition, error) {
	var body LocalNodeTransition
	switch c := command.(type) {
	case store.StartNode:
		body.To, body.Cause, body.SourceAttempt = store.NodeRunning, store.CauseStart, c.SourceAttempt
	case store.RetryNode:
		body.To, body.Cause, body.SourceAttempt, body.Extra.Result, body.Extra.RetryAt = store.NodeRunning, store.CauseRetry, c.SourceAttempt, c.Result, c.RetryAt
	case store.ReportNode:
		body.To, body.Cause, body.SourceAttempt, body.Extra.Result = c.Outcome, store.CauseReport, c.SourceAttempt, c.Result
	case store.ReadyNode:
		body.To, body.Cause = store.NodeReady, store.CauseRelease
	case store.ReleaseNode:
		body.To, body.Cause, body.SourceAttempt = store.NodePending, store.CauseRelease, c.SourceAttempt
	case store.CancelNode:
		body.To, body.Cause, body.Extra.Result = store.NodeCancelled, store.CauseCancel, c.Result
	case store.DependencyNode:
		body.To, body.Cause, body.Extra.Result = c.Outcome, store.CauseDependency, c.Result
	case store.OwnerPolicyNode:
		body.To, body.Cause, body.Extra.Result, body.Extra.OwnerPolicy = c.Outcome, store.CauseOwnerPolicy, c.Result, c.OwnerPolicy
	case store.OpenApprovalNode:
		body.To, body.Cause = store.NodeApprovalPending, store.CauseApprovalOpen
	case store.ResolveApprovalNode:
		body.To, body.Cause, body.Extra.Approval = c.Outcome, store.CauseApprovalResolve, c.Approval
	case store.TimeoutApprovalNode:
		body.To, body.Cause = store.NodeFailed, store.CauseApprovalTimeout
	case store.MaterializeNode:
		body.To, body.Cause, body.Extra.Materialize = "", store.CauseMaterialize, c.Materialize
	default:
		return body, fmt.Errorf("node command is unavailable on local transport")
	}
	return body, nil
}
