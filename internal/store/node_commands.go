package store

import (
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// NodeCommand is a closed operation accepted by the node state machine.
// Each command fixes its edge and carries only that edge's payload.
type NodeCommand interface {
	nodeChange() nodeTransition
	bind(controllerwire.NodeRef, Actor) NodeCommand
}

func (c nodeTransition) nodeChange() nodeTransition { return c }
func (c nodeTransition) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

// NodeCommandAuthority returns the target and captured authority for transport selection.
func NodeCommandAuthority(c NodeCommand) (controllerwire.NodeRef, Actor) {
	if nilNodeCommand(c) {
		return controllerwire.NodeRef{}, Actor{}
	}
	change := c.nodeChange()
	return change.Ref, change.Actor
}

// BindNodeCommand applies authenticated transport authority to an operation.
func BindNodeCommand(c NodeCommand, ref controllerwire.NodeRef, actor Actor) NodeCommand {
	if nilNodeCommand(c) {
		return nil
	}
	return c.bind(ref, actor)
}

// StartNode requests the start edge.
type StartNode struct {
	Ref           controllerwire.NodeRef
	Actor         Actor
	SourceAttempt *runspec.AttemptOrdinal
	Attempt       *NodeAttempt
}

func (c StartNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, SourceAttempt: c.SourceAttempt, To: NodeRunning, Cause: CauseStart, Extra: transitionExtra{Attempt: c.Attempt}}
}

// RetryNode requests the retry edge.
type RetryNode struct {
	Ref           controllerwire.NodeRef
	Actor         Actor
	SourceAttempt *runspec.AttemptOrdinal
	Result        *NodeResult
	RetryAt       *time.Time
}

func (c RetryNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, SourceAttempt: c.SourceAttempt, To: NodeRunning, Cause: CauseRetry, Extra: transitionExtra{Result: c.Result, RetryAt: c.RetryAt}}
}

// ReportNode requests the report edge.
type ReportNode struct {
	Ref           controllerwire.NodeRef
	Actor         Actor
	SourceAttempt *runspec.AttemptOrdinal
	Outcome       NodeState
	Result        *NodeResult
}

func (c ReportNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, SourceAttempt: c.SourceAttempt, To: c.Outcome, Cause: CauseReport, Extra: transitionExtra{Result: c.Result}}
}

// ReadyNode requests the release edge.
type ReadyNode struct {
	Ref   controllerwire.NodeRef
	Actor Actor
}

func (c ReadyNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: NodeReady, Cause: CauseRelease, Extra: transitionExtra{}}
}

// ReleaseNode requests the release edge.
type ReleaseNode struct {
	Ref           controllerwire.NodeRef
	Actor         Actor
	SourceAttempt *runspec.AttemptOrdinal
}

func (c ReleaseNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, SourceAttempt: c.SourceAttempt, To: NodePending, Cause: CauseRelease, Extra: transitionExtra{}}
}

// CancelNode requests the cancel edge.
type CancelNode struct {
	Ref    controllerwire.NodeRef
	Actor  Actor
	Result *NodeResult
}

func (c CancelNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: NodeCancelled, Cause: CauseCancel, Extra: transitionExtra{Result: c.Result}}
}

// DependencyNode requests the dependency edge.
type DependencyNode struct {
	Ref     controllerwire.NodeRef
	Actor   Actor
	Outcome NodeState
	Result  *NodeResult
}

func (c DependencyNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: c.Outcome, Cause: CauseDependency, Extra: transitionExtra{Result: c.Result}}
}

// OwnerPolicyNode requests the ownerpolicy edge.
type OwnerPolicyNode struct {
	Ref         controllerwire.NodeRef
	Actor       Actor
	Outcome     NodeState
	Result      *NodeResult
	OwnerPolicy *OwnerPolicyResult
}

func (c OwnerPolicyNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: c.Outcome, Cause: CauseOwnerPolicy, Extra: transitionExtra{Result: c.Result, OwnerPolicy: c.OwnerPolicy}}
}

// OpenApprovalNode requests the approvalopen edge.
type OpenApprovalNode struct {
	Ref   controllerwire.NodeRef
	Actor Actor
}

func (c OpenApprovalNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: NodeApprovalPending, Cause: CauseApprovalOpen, Extra: transitionExtra{}}
}

// ResolveApprovalNode requests the approvalresolve edge.
type ResolveApprovalNode struct {
	Ref      controllerwire.NodeRef
	Actor    Actor
	Outcome  NodeState
	Approval *ApprovalResolution
}

func (c ResolveApprovalNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: c.Outcome, Cause: CauseApprovalResolve, Extra: transitionExtra{Approval: c.Approval}}
}

// TimeoutApprovalNode requests the approvaltimeout edge.
type TimeoutApprovalNode struct {
	Ref   controllerwire.NodeRef
	Actor Actor
}

func (c TimeoutApprovalNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: "", Cause: CauseApprovalTimeout, Extra: transitionExtra{}}
}

// MaterializeNode requests the materialize edge.
type MaterializeNode struct {
	Ref         controllerwire.NodeRef
	Actor       Actor
	Materialize *MaterializeSource
}

func (c MaterializeNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: "", Cause: CauseMaterialize, Extra: transitionExtra{Materialize: c.Materialize}}
}

// PublishNode requests the publish edge.
type PublishNode struct {
	Ref     controllerwire.NodeRef
	Actor   Actor
	Outcome NodeState
	Result  *NodeResult
}

func (c PublishNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: c.Outcome, Cause: CausePublish, Extra: transitionExtra{Result: c.Result}}
}

// PublishNodeAttempt requests the publishattempt edge.
type PublishNodeAttempt struct {
	Ref         controllerwire.NodeRef
	Actor       Actor
	Publication *runspec.PublishedAttempt
}

func (c PublishNodeAttempt) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: NodePending, Cause: CausePublishAttempt, Extra: transitionExtra{Publication: c.Publication}}
}

func (c StartNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c RetryNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c ReportNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c ReadyNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c ReleaseNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c CancelNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c DependencyNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c OwnerPolicyNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c OpenApprovalNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c ResolveApprovalNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c TimeoutApprovalNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c MaterializeNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c PublishNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func (c PublishNodeAttempt) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

// RevokeNode withdraws an unstarted offer under its owner authority.
type RevokeNode struct {
	Ref   controllerwire.NodeRef
	Actor Actor
}

func (c RevokeNode) nodeChange() nodeTransition {
	return nodeTransition{Ref: c.Ref, Actor: c.Actor, To: NodePending, Cause: CauseRevoke}
}

func (c RevokeNode) bind(ref controllerwire.NodeRef, actor Actor) NodeCommand {
	c.Ref, c.Actor = ref, actor
	return c
}

func nilNodeCommand(command NodeCommand) bool {
	if command == nil {
		return true
	}
	value := reflect.ValueOf(command)
	return value.Kind() == reflect.Pointer && value.IsNil()
}
