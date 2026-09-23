package controller

import (
	"context"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func classifyExecutionLocation(principal, holder, runner string, metered bool) (kind, name string) {
	if strings.HasPrefix(principal, store.GitHubRunnerPrincipalPrefix) {
		_, repo, found := strings.Cut(strings.TrimPrefix(principal, store.GitHubRunnerPrincipalPrefix), ":")
		if found {
			return "github-actions", repo
		}
		return "github-actions", ""
	}
	if metered {
		return "cloud", ""
	}
	if strings.HasPrefix(holder, "k8s-job:") || strings.HasPrefix(holder, "pod:") {
		if runner == "" {
			runner = "warm pool"
		}
		return "cluster", runner
	}
	if runner == "" {
		runner = strings.TrimPrefix(principal, "agent:")
	}
	if runner != "" {
		return "machine", runner
	}
	return "", ""
}

func (s *Server) publicNodesWithExecutionLocation(ctx context.Context, nodes []*store.Node) ([]*store.Node, error) {
	out := make([]*store.Node, len(nodes))
	for i, node := range nodes {
		public := api.PublicNode(node)
		if node.ClaimedBy == "" {
			out[i] = public
			continue
		}
		principal, holder, runner, metered, err := s.store.NodeClaimOrigin(ctx, node.RunID, node.NodeID)
		if err != nil {
			return nil, err
		}
		kind, name := classifyExecutionLocation(principal, holder, runner, metered)
		public.ExecutionSite, public.ExecutionSiteName = kind, name
		for j, attempt := range node.ExecutionAttempts {
			if holder != "" && attempt.HolderID == holder {
				public.ExecutionAttempts[j].ExecutionSite = kind
				public.ExecutionAttempts[j].ExecutionSiteName = name
			}
		}
		out[i] = public
	}
	return out, nil
}
