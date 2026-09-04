package store

import (
	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
)

const (
	assistedExecutionPolicyRequirement = "assisted-execution-policy-v1"
	nodeExecutionPolicyVersion         = executionpolicy.NodeExecutionPolicyVersion
	assistedBodyProtocolVersion        = executionpolicy.AssistedBodyProtocolVersion
	fleetSupervisorRuntimeRequirement  = "fleet-supervisor-v1"
	fleetBodyRuntimeRequirement        = "fleet-body-v1"
)

var (
	errExecutionPolicyInvalid   = executionpolicy.ErrExecutionPolicyInvalid
	errExecutionPolicyConflict  = executionpolicy.ErrExecutionPolicyConflict
	errExecutionUpgradeRequired = executionpolicy.ErrExecutionUpgradeRequired
)

type (
	executionPolicyPersistence = executionpolicy.Persistence
	nodeExecutionPolicy        = executionpolicy.NodeExecutionPolicy
	nodeDependencyAuthority    = executionpolicy.NodeDependencyAuthority
	nodeCompiledBodyAuthority  = executionpolicy.NodeCompiledBodyAuthority
	nodeBodySourceAuthority    = executionpolicy.NodeBodySourceAuthority
	sealedExecutionPolicy      = executionpolicy.Sealed
)

type nodeRecord struct {
	Node
	executionpolicy.Carrier
}

func sealExecutionPolicy(policy nodeExecutionPolicy) (sealedExecutionPolicy, error) {
	return executionpolicy.SealNew(policy)
}

func restoreNodeExecutionPolicySeal(node *nodeRecord, policyJSON []byte, policyHash string, policyVersion, bodyProtocol int,
	supervisorJSON []byte, supervisorHash string, bodyJSON []byte, bodyHash string,
) error {
	return executionpolicy.Restore(node, executionpolicy.Persistence{
		PolicyJSON: policyJSON, PolicyHash: policyHash,
		PolicyVersion: policyVersion, BodyProtocol: bodyProtocol,
		SupervisorRequirementsJSON: supervisorJSON, SupervisorRequirementsHash: supervisorHash,
		BodyRequirementsJSON: bodyJSON, BodyRequirementsHash: bodyHash,
	})
}

func validateNodeExecutionPolicy(node *nodeRecord, pipeline string) error {
	return executionpolicy.ValidateForNode(node, executionpolicy.Binding{
		Pipeline: pipeline, NodeID: node.NodeID, Dependencies: node.Deps,
		NeedsLabels: node.NeedsLabels, RequiredExecutorLocation: node.RequiredExecutorLocation,
	})
}

func setNodeExecutionPolicy(node *nodeRecord, policy nodeExecutionPolicy) error {
	return executionpolicy.SetNew(node, policy)
}

func nodeExecutionPolicyPersistence(node *nodeRecord) (executionpolicy.Persistence, error) {
	return executionpolicy.PersistenceOf(node)
}

func nodeHasExecutionPolicy(node *nodeRecord) bool {
	return executionpolicy.IsSealed(node)
}

func policyForNodeExecution(node *nodeRecord) (nodeExecutionPolicy, bool, error) {
	return executionpolicy.PolicyOf(node)
}

func claimBindingForNodeExecution(node *nodeRecord) (executionpolicy.ClaimBinding, error) {
	return executionpolicy.ClaimBindingOf(node, node.RunID, node.NodeID)
}

func copyNodeExecutionPolicy(dst, src *nodeRecord) {
	executionpolicy.CopyCarrier(dst, src)
}

func nodeExecutionPoliciesEqual(left, right *nodeRecord) bool {
	return executionpolicy.Equal(left, right)
}
