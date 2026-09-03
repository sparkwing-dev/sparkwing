package controller

import (
	"context"
	"errors"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// NodeClaimPolicy resolves authenticated executor registration and capacity
// into a pure snapshot the store can use while holding an award transaction.
type NodeClaimPolicy interface {
	Resolver(context.Context, store.ClaimIdentity, NodeClaimRequest) (store.NodeClaimResolver, error)
	HighestEligiblePriority(context.Context, *store.Node) (int, error)
}

// NodeClaimRequest is the executor-supplied half of a claim offer. A policy
// replaces identity, kind, and priority with their registered values.
type NodeClaimRequest struct {
	HolderID      string
	WorkerID      string
	ExecutorKind  string
	ReservationID string
	ClaimPriority int
	Labels        []string
}

// WithNodeClaimPolicy connects durable worker registration and contribution
// admission to claim arbitration.
func (s *Server) WithNodeClaimPolicy(policy NodeClaimPolicy) *Server {
	s.nodeClaimPolicy = policy
	return s
}

type requestNodeClaimPolicy struct{}

func (requestNodeClaimPolicy) Resolver(_ context.Context, claimant store.ClaimIdentity, req NodeClaimRequest) (store.NodeClaimResolver, error) {
	if req.ClaimPriority < 0 || req.ClaimPriority > 100 {
		return nil, errors.New("claim_priority must be between 0 and 100")
	}
	workerID := strings.TrimSpace(req.WorkerID)
	if workerID == "" {
		workerID = claimant.TokenPrefix
	}
	if workerID == "" {
		workerID = req.HolderID
	}
	labels := make(map[string]struct{}, len(req.Labels))
	for _, label := range req.Labels {
		label = strings.TrimSpace(label)
		if label != "" {
			labels[label] = struct{}{}
		}
	}
	return store.NodeClaimResolverFunc(func(node *store.Node) (store.NodeClaimResolution, bool) {
		if !claimLabelsSatisfied(node.NeedsLabels, labels) {
			return store.NodeClaimResolution{}, false
		}
		return store.NodeClaimResolution{
			WorkerID:          workerID,
			ExecutorKind:      req.ExecutorKind,
			ReservationID:     req.ReservationID,
			BasePriority:      req.ClaimPriority,
			EffectivePriority: req.ClaimPriority,
		}, true
	}), nil
}

func (requestNodeClaimPolicy) HighestEligiblePriority(context.Context, *store.Node) (int, error) {
	return 100, nil
}

func claimLabelsSatisfied(needed []string, have map[string]struct{}) bool {
	for _, term := range needed {
		matched := false
		for _, alternative := range strings.Split(term, ",") {
			if _, ok := have[strings.TrimSpace(alternative)]; ok {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
