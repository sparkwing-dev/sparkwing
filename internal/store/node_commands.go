package store

import (
	"fmt"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// NodeCommand is a closed operation accepted by the node state machine.
// Each command fixes its edge and carries only that edge's payload.
type NodeCommand interface {
	nodeChange() nodeTransition
}

func (c nodeTransition) nodeChange() nodeTransition { return c }

// NodeCommandAuthority returns the target and captured authority for transport selection.
func NodeCommandAuthority(c NodeCommand) (runspec.NodeRef, runstate.Actor) {
	if nilNodeCommand(c) {
		return runspec.NodeRef{}, runstate.Actor{}
	}
	change := c.nodeChange()
	return change.Ref, change.Actor
}

// BindNodeCommand applies authenticated transport authority to an operation.
func BindNodeCommand(c NodeCommand, ref runspec.NodeRef, actor runstate.Actor) NodeCommand {
	if nilNodeCommand(c) {
		return nil
	}
	change := c.nodeChange()
	change.Ref, change.Actor = ref, actor
	return change
}

// StartNode requests the start edge.
type StartNode struct {
	Ref           runspec.NodeRef
	Actor         runstate.Actor
	SourceAttempt *runspec.AttemptOrdinal
	Attempt       *runstate.NodeAttempt
}

func (c StartNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, SourceAttempt: c.SourceAttempt, To: runstate.NodeRunning, Cause: runstate.CauseStart, Extra: transitionExtra{Attempt: c.Attempt}}
}

// RetryNode requests the retry edge.
type RetryNode struct {
	Ref           runspec.NodeRef
	Actor         runstate.Actor
	SourceAttempt *runspec.AttemptOrdinal
	Result        *runstate.NodeResult
	RetryAt       *time.Time
}

func (c RetryNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, SourceAttempt: c.SourceAttempt, To: runstate.NodeRunning, Cause: runstate.CauseRetry, Extra: transitionExtra{Result: c.Result, RetryAt: c.RetryAt}}
}

// ReportNode requests the report edge.
type ReportNode struct {
	Ref           runspec.NodeRef
	Actor         runstate.Actor
	SourceAttempt *runspec.AttemptOrdinal
	Outcome       runstate.NodeState
	Result        *runstate.NodeResult
}

func (c ReportNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, SourceAttempt: c.SourceAttempt, To: c.Outcome, Cause: runstate.CauseReport, Extra: transitionExtra{Result: c.Result}}
}

// ReadyNode requests the release edge.
type ReadyNode struct {
	Ref   runspec.NodeRef
	Actor runstate.Actor
}

func (c ReadyNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: runstate.NodeReady, Cause: runstate.CauseRelease, Extra: transitionExtra{}}
}

// ReleaseNode requests the release edge.
type ReleaseNode struct {
	Ref           runspec.NodeRef
	Actor         runstate.Actor
	SourceAttempt *runspec.AttemptOrdinal
}

func (c ReleaseNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, SourceAttempt: c.SourceAttempt, To: nodePending, Cause: runstate.CauseRelease, Extra: transitionExtra{}}
}

// CancelNode requests the cancel edge.
type CancelNode struct {
	Ref    runspec.NodeRef
	Actor  runstate.Actor
	Result *runstate.NodeResult
}

func (c CancelNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: runstate.NodeCancelled, Cause: causeCancel, Extra: transitionExtra{Result: c.Result}}
}

// DependencyNode requests the dependency edge.
type DependencyNode struct {
	Ref     runspec.NodeRef
	Actor   runstate.Actor
	Outcome runstate.NodeState
	Result  *runstate.NodeResult
}

func (c DependencyNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: c.Outcome, Cause: causeDependency, Extra: transitionExtra{Result: c.Result}}
}

// OwnerPolicyNode requests the ownerpolicy edge.
type OwnerPolicyNode struct {
	Ref         runspec.NodeRef
	Actor       runstate.Actor
	Outcome     runstate.NodeState
	Result      *runstate.NodeResult
	OwnerPolicy *runstate.OwnerPolicyResult
}

func (c OwnerPolicyNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: c.Outcome, Cause: causeOwnerPolicy, Extra: transitionExtra{Result: c.Result, OwnerPolicy: c.OwnerPolicy}}
}

// OpenApprovalNode requests the approvalopen edge.
type OpenApprovalNode struct {
	Ref   runspec.NodeRef
	Actor runstate.Actor
}

func (c OpenApprovalNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: runstate.NodeApprovalPending, Cause: runstate.CauseApprovalOpen, Extra: transitionExtra{}}
}

// ResolveApprovalNode requests the approvalresolve edge.
type ResolveApprovalNode struct {
	Ref      runspec.NodeRef
	Actor    runstate.Actor
	Outcome  runstate.NodeState
	Approval *runstate.ApprovalResolution
}

func (c ResolveApprovalNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: c.Outcome, Cause: causeApprovalResolve, Extra: transitionExtra{Approval: c.Approval}}
}

// TimeoutApprovalNode requests the approvaltimeout edge.
type TimeoutApprovalNode struct {
	Ref   runspec.NodeRef
	Actor runstate.Actor
}

func (c TimeoutApprovalNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: "", Cause: causeApprovalTimeout, Extra: transitionExtra{}}
}

// materializeNode requests the materialize edge.
type materializeNode struct {
	Ref         runspec.NodeRef
	Actor       runstate.Actor
	Materialize *runstate.MaterializeSource
}

func (c materializeNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: "", Cause: causeMaterialize, Extra: transitionExtra{Materialize: c.Materialize}}
}

// publishNode requests the publish edge.
type publishNode struct {
	Ref     runspec.NodeRef
	Actor   runstate.Actor
	Outcome runstate.NodeState
	Result  *runstate.NodeResult
}

func (c publishNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: c.Outcome, Cause: runstate.CausePublish, Extra: transitionExtra{Result: c.Result}}
}

// publishNodeAttempt requests the publishattempt edge.
type publishNodeAttempt struct {
	Ref         runspec.NodeRef
	Actor       runstate.Actor
	Publication *runspec.PublishedAttempt
}

func (c publishNodeAttempt) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: nodePending, Cause: runstate.CausePublishAttempt, Extra: transitionExtra{Publication: c.Publication}}
}

// RevokeNode withdraws an unstarted offer under its owner authority.
type RevokeNode struct {
	Ref   runspec.NodeRef
	Actor runstate.Actor
}

func (c RevokeNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: nodePending, Cause: runstate.CauseRevoke}
}

func nilNodeCommand(command NodeCommand) bool {
	if command == nil {
		return true
	}
	value := reflect.ValueOf(command)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

// NodeCommandFromPayload validates the payload and binds authenticated authority.
func NodeCommandFromPayload(body runstate.NodeCommandPayload, ref runspec.NodeRef, actor runstate.Actor) (NodeCommand, error) {
	var command NodeCommand
	switch body.Cause {
	case runstate.CauseStart:
		command = StartNode{Ref: ref, Actor: actor, SourceAttempt: body.SourceAttempt}
	case runstate.CauseRetry:
		command = RetryNode{Ref: ref, Actor: actor, SourceAttempt: body.SourceAttempt, Result: body.Extra.Result, RetryAt: body.Extra.RetryAt}
	case runstate.CauseReport:
		command = ReportNode{Ref: ref, Actor: actor, SourceAttempt: body.SourceAttempt, Outcome: body.To, Result: body.Extra.Result}
	case runstate.CauseRelease:
		if body.To == runstate.NodeReady {
			command = ReadyNode{Ref: ref, Actor: actor}
		} else {
			command = ReleaseNode{Ref: ref, Actor: actor, SourceAttempt: body.SourceAttempt}
		}
	case causeCancel:
		command = CancelNode{Ref: ref, Actor: actor, Result: body.Extra.Result}
	case causeDependency:
		command = DependencyNode{Ref: ref, Actor: actor, Outcome: body.To, Result: body.Extra.Result}
	case causeOwnerPolicy:
		command = OwnerPolicyNode{Ref: ref, Actor: actor, Outcome: body.To, Result: body.Extra.Result, OwnerPolicy: body.Extra.OwnerPolicy}
	case runstate.CauseApprovalOpen:
		command = OpenApprovalNode{Ref: ref, Actor: actor}
	case causeApprovalResolve:
		command = ResolveApprovalNode{Ref: ref, Actor: actor, Outcome: body.To, Approval: body.Extra.Approval}
	case causeApprovalTimeout:
		command = TimeoutApprovalNode{Ref: ref, Actor: actor}
	case causeMaterialize:
		command = materializeNode{Ref: ref, Actor: actor, Materialize: body.Extra.Materialize}
	case runstate.CausePublish:
		command = publishNode{Ref: ref, Actor: actor, Outcome: body.To, Result: body.Extra.Result}
	case runstate.CausePublishAttempt:
		command = publishNodeAttempt{Ref: ref, Actor: actor, Publication: body.Extra.Publication}
	case runstate.CauseRevoke:
		command = RevokeNode{Ref: ref, Actor: actor}
	default:
		return nil, fmt.Errorf("unsupported node command %q", body.Cause)
	}
	encoded, err := NodeCommandPayloadFor(command)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(body, encoded) {
		return nil, fmt.Errorf("node command carries conflicting fields")
	}
	return command, nil
}

// NodeCommandPayloadFor omits authenticated authority and server-owned attempt facts.
func NodeCommandPayloadFor(command NodeCommand) (runstate.NodeCommandPayload, error) {
	if nilNodeCommand(command) {
		return runstate.NodeCommandPayload{}, fmt.Errorf("node command is required")
	}
	c := command.nodeChange()
	return runstate.NodeCommandPayload{To: c.To, Cause: c.Cause, SourceAttempt: c.SourceAttempt, Extra: runstate.NodeCommandExtra{
		OwnerPolicy: c.Extra.OwnerPolicy, Result: c.Extra.Result, Materialize: c.Extra.Materialize, Approval: c.Extra.Approval, RetryAt: c.Extra.RetryAt, Publication: c.Extra.Publication,
	}}, nil
}
