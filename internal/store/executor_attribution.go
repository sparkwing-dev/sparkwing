package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func attemptExecutorTx(ctx context.Context, tx *storeTx, actor Actor, observed *NodeAttempt) (kind, name, location string, err error) {
	chain, err := authorityChainTx(ctx, tx, actor.Authority, false)
	if err != nil {
		return "", "", "", err
	}
	leaf := chain[0].principal.Capability
	root := chain[len(chain)-1].principal.Capability
	kind, name, location = "agent", root.Subject.ID, "local"
	for _, ancestor := range chain {
		c := ancestor.principal.Capability
		if push := c.Binding.Push; c.Binding.Kind == capability.BindingPush && push != nil {
			name = push.RepositoryKey
			err := tx.QueryRowContext(ctx, `SELECT repository FROM github_runner_bindings WHERE team=? AND repo_key=?`, c.Team, push.RepositoryKey).Scan(&name)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return "", "", "", err
			}
			return "github-actions", name + " run " + push.WorkflowRunID, "cloud", nil
		}
		if c.Purpose == capability.PurposeHere {
			kind = "local"
		}
	}
	if root.Subject.Kind == capability.SubjectLocal {
		kind = "local"
	}
	if leaf.Billing != nil && leaf.Billing.Metered {
		location = "cloud"
	}
	if leaf.Billing != nil && leaf.Billing.ExecutorClass == "cloud" {
		kind, name, location = "kubernetes", leaf.Binding.Holder, "cloud"
	}
	if observed != nil && (kind == "local" || kind == "kubernetes") && observed.ExecutorName != "" {
		if len(observed.ExecutorName) > maxExecutionExecutorNameLen {
			return "", "", "", ErrInvalidInput
		}
		name = safeExecutorName(observed.ExecutorName)
	}
	return kind, name, location, nil
}
