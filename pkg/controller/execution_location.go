package controller

import (
	"context"
	"database/sql"
	"errors"
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
		if runner == "" && strings.HasPrefix(principal, "agent:") {
			runner = strings.TrimPrefix(principal, "agent:")
		}
		if runner == "" && strings.HasPrefix(holder, "pod:") {
			runner = strings.TrimPrefix(holder, "pod:")
		}
		if runner == "" && strings.HasPrefix(holder, "runner:") {
			runner, _, _ = strings.Cut(strings.TrimPrefix(holder, "runner:"), ":")
		}
		if runner == "" && strings.HasPrefix(holder, "agent:") {
			runner, _, _ = strings.Cut(strings.TrimPrefix(holder, "agent:"), ":")
		}
		return "cloud", runner
	}
	if strings.HasPrefix(holder, "k8s-job:") {
		if runner == "" {
			runner = strings.TrimPrefix(holder, "k8s-job:")
		}
		return "cluster", runner
	}
	if strings.HasPrefix(holder, "pod:") {
		if runner == "" {
			runner = strings.TrimPrefix(holder, "pod:")
		}
		return "cluster", runner
	}
	if runner == "" && strings.HasPrefix(principal, "agent:") {
		runner = strings.TrimPrefix(principal, "agent:")
	}
	if runner == "" && strings.HasPrefix(holder, "agent:") {
		runner, _, _ = strings.Cut(strings.TrimPrefix(holder, "agent:"), ":")
	}
	if runner == "" && strings.HasPrefix(holder, "runner:") {
		runner, _, _ = strings.Cut(strings.TrimPrefix(holder, "runner:"), ":")
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
		var principal, holder, runner string
		var metered bool
		if node.ClaimedBy != "" {
			var err error
			principal, holder, runner, metered, err = s.store.NodeClaimOrigin(ctx, node.RunID, node.NodeID)
			if err != nil {
				return nil, err
			}
		}
		kind, name := classifyExecutionLocation(principal, holder, runner, metered)
		public.ExecutionSite, public.ExecutionSiteName = kind, name
		for j, attempt := range node.ExecutionAttempts {
			attemptKind, attemptName := classifyExecutionLocation("", attempt.HolderID, attempt.ExecutorName, attempt.ExecutorLocation == "cloud")
			if attemptName == "" && strings.HasPrefix(attempt.HolderID, "trigger:") {
				triggerPrincipal, triggerMetered, err := s.store.TriggerClaimOrigin(ctx, attempt.RunID, attempt.ClaimGeneration)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
				if err == nil {
					originKind, originName := classifyExecutionLocation(triggerPrincipal, "", "", triggerMetered)
					if attempt.ExecutorLocation == "cloud" && originKind == "machine" {
						attemptName = originName
					} else {
						attemptKind, attemptName = originKind, originName
					}
				}
			}
			switch attempt.ExecutorKind {
			case "github-actions":
				attemptKind, attemptName = "github-actions", attempt.ExecutorName
			case "kubernetes":
				attemptKind, attemptName = "cluster", attempt.ExecutorName
			case "cloud":
				attemptKind, attemptName = "cloud", attempt.ExecutorName
			}
			if holder != "" && attempt.HolderID == holder && attempt.ExecutorName == "" {
				if attemptKind == "cloud" && kind == "machine" {
					attemptName = name
				} else if kind != "" {
					attemptKind, attemptName = kind, name
				}
			}
			public.ExecutionAttempts[j].ExecutionSite = attemptKind
			public.ExecutionAttempts[j].ExecutionSiteName = attemptName
			if j == len(node.ExecutionAttempts)-1 && public.ExecutionSite == "" {
				public.ExecutionSite, public.ExecutionSiteName = attemptKind, attemptName
			}
		}
		out[i] = public
	}
	return out, nil
}
