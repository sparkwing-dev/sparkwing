package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// safety: only the owner of a host-dispatched run claims its jobs, so an ineligible ready job never runs;
// this names the cause instead of leaving it pending without a detail.
func readHostIneligibleTx(ctx context.Context, tx *storeTx, team Team, snapshot RunSnapshot) (map[string]string, error) {
	run := snapshot.Run
	if run.FinishedAt != nil || run.CancelRequestedAt != nil || run.Request.Request.Dispatch != runspec.DispatchHost || run.Request.Request.Burst != nil {
		return nil, nil
	}
	var waiting []runstate.NodeRow
	for _, node := range snapshot.Nodes {
		if node.Status == runstate.StatusPending && node.ReadyAt != nil && node.ClaimedBy == "" {
			waiting = append(waiting, node)
		}
	}
	if len(waiting) == 0 {
		return nil, nil
	}
	now := tx.now(ctx).UnixNano()
	var prefix string
	err := tx.QueryRowContext(ctx, `SELECT capability_prefix FROM triggers WHERE team=? AND id=? AND status='claimed' AND lease_expires_at>?`, team, run.ID, now).Scan(&prefix)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	chain, err := teamAuthorityChainTx(ctx, tx, team, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, false, sameTeam)
	if err != nil || len(chain) == 0 {
		return nil, nil //nolint:nilerr // safety: an owner whose authority lapsed mid-read is transient and must not fail the run page.
	}
	owner := chain[0].principal
	demand, observed, err := ownerExecutionDemandTx(ctx, tx, owner)
	if err != nil || !observed {
		return nil, err
	}
	eligible, err := ownerEligibleNodesTx(ctx, tx, owner, demand)
	if err != nil {
		return nil, err
	}
	reasons := map[string]string{}
	for _, node := range waiting {
		if eligible[node.Ref] {
			continue
		}
		reason := "waiting: the machine hosting this run cannot execute this job, and a local run executes only on that machine"
		if requires := node.Execution.Requires.Value; len(requires) > 0 {
			reported := "none"
			if len(demand.Labels) > 0 {
				reported = strings.Join(demand.Labels, ", ")
			}
			reason = fmt.Sprintf("waiting: requires runner labels %s, which the machine hosting this run does not report (it reports: %s); a local run executes only on that machine, so cancel it and run it where those labels apply, or change the job's requirements", strings.Join(requires, ", "), reported)
		}
		reasons[string(node.Ref.NodeID)] = reason
	}
	return reasons, nil
}
