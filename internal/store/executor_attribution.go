package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// safety: trusted says the attempt's host may produce a counted pass: today a fresh host, or a
// root whose admin-set trust excludes asserted runs. Only the claimant's authority chain
// decides it, never a name the runner reports.
func attemptExecutorTx(ctx context.Context, tx *storeTx, actor runstate.Actor, observed *runstate.NodeAttempt) (kind, name, location string, trusted bool, err error) {
	chain, err := authorityChainTx(ctx, tx, actor.Authority, false)
	if err != nil {
		return "", "", "", false, err
	}
	facts, err := claimantFactsTx(ctx, tx, chain, runspec.Request{})
	if err != nil {
		return "", "", "", false, err
	}
	leaf := chain[0].principal.Capability
	root := chain[len(chain)-1].principal.Capability
	trusted = facts.FreshHost || root.Subject.Kind == capability.SubjectMachine && root.Trust.ExcludesAsserted()
	kind, name, location = "agent", root.Subject.ID, "local"
	for _, ancestor := range chain {
		c := ancestor.principal.Capability
		if push := c.Binding.Push; c.Binding.Kind == capability.BindingPush && push != nil {
			name = push.RepositoryKey
			err := tx.QueryRowContext(ctx, `SELECT repository FROM github_runner_bindings WHERE team=? AND repo_key=?`, c.Team, push.RepositoryKey).Scan(&name)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return "", "", "", false, err
			}
			return tierActions, name + " run " + push.WorkflowRunID, TierCloud, trusted, nil
		}
	}
	if facts.ExecutorClass == "local" || facts.ExecutorClass == tierHere {
		kind = "local"
	}
	if leaf.Billing != nil && leaf.Billing.Metered {
		location = TierCloud
	}
	if facts.Cloud {
		kind, name, location = "kubernetes", leaf.Binding.Holder, TierCloud
	}
	if observed != nil && (kind == "local" || kind == "kubernetes") && observed.ExecutorName != "" {
		if len(observed.ExecutorName) > maxExecutionExecutorNameLen {
			return "", "", "", false, ErrInvalidInput
		}
		name = safeExecutorName(observed.ExecutorName)
	}
	return kind, name, location, trusted, nil
}
