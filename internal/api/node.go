package api

import (
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// PublicNode returns the credential-safe shape shared by node read surfaces.
func PublicNode(node *store.Node) *store.Node {
	if node == nil {
		return nil
	}
	out := *node
	out.Claimed = out.Claimed || out.ClaimedBy != ""
	if (out.ExecutorKind == "agent" || out.ExecutorKind == "gateway") && out.ClaimWorkerID != "" {
		out.ExecutorName = out.ClaimWorkerID
	}
	out.ClaimedBy = ""
	out.ClaimWorkerID = ""
	out.ClaimExecutorKind = ""
	out.ClaimReservationID = ""
	out.CoordinatorID = ""
	out.ClaimGeneration = 0
	out.ClaimMembershipID = ""
	out.ExecutorID = ""
	out.RequiredCoordinatorID = ""
	out.ReservationID = ""
	out.AvoidCoordinatorID = ""
	out.AvoidExecutorKind = ""
	out.AvoidExecutorID = ""
	out.AvoidUntil = nil
	if node.ExecutionAttempts != nil {
		out.ExecutionAttempts = make([]store.ExecutionAttempt, len(node.ExecutionAttempts))
		for i, attempt := range node.ExecutionAttempts {
			attempt.ClaimGeneration = 0
			attempt.CoordinatorID = ""
			attempt.MembershipID = ""
			attempt.ExecutorID = ""
			attempt.HolderID = ""
			attempt.ReservationID = ""
			out.ExecutionAttempts[i] = attempt
		}
	}
	return &out
}

func PublicNodes(nodes []*store.Node) []*store.Node {
	if nodes == nil {
		return nil
	}
	out := make([]*store.Node, len(nodes))
	for i, node := range nodes {
		out[i] = PublicNode(node)
	}
	return out
}
