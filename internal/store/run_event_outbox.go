package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runevents"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// safety: runs inside the run transition, so a state change and its deliveries commit or roll back together.
func writeRunEventDeliveriesTx(ctx context.Context, tx *storeTx, team Team, row RunRow, kind string, change runstate.RunTransition, now time.Time) error {
	var event string
	switch {
	case kind == "run_started":
		event = runevents.EventStarted
	case kind == "run_"+string(row.State) && row.FinishedAt != nil:
		event = runevents.EventFinished
	default:
		return nil
	}
	subs, err := runEventSubscriptionsTx(ctx, tx, team)
	if err != nil || len(subs) == 0 {
		return err
	}
	var trigger, branch, sha, repoKey, repoDisplay, retryOf string
	var parent sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT trigger_source,git_branch,git_sha,repo_key,repo_display,retry_of,parent_run_id FROM runs WHERE team=? AND id=?`, team, row.ID).Scan(&trigger, &branch, &sha, &repoKey, &repoDisplay, &retryOf, &parent); err != nil {
		return err
	}
	if strings.HasPrefix(repoKey, "local:") {
		// safety: a local repository's display is its checkout path, which events never carry.
		repoDisplay = ""
	}
	var matched []RunEventSubscription
	for _, sub := range subs {
		if sub.Filter.Matches(event, string(row.State), row.Pipeline, repoKey) {
			matched = append(matched, sub)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	source, err := runEventSourceTx(ctx, tx, team)
	if err != nil {
		return err
	}
	facts := runevents.Facts{
		Source:            source,
		StateSeq:          row.StateSeq,
		Time:              now,
		RunID:             row.ID,
		RunNumber:         row.Number,
		Pipeline:          row.Pipeline,
		Status:            string(row.State),
		RepositoryKey:     repoKey,
		RepositoryDisplay: repoDisplay,
		GitBranch:         branch,
		GitSHA:            sha,
		Trigger:           trigger,
		CreatedAt:         row.CreatedAt,
		StartedAt:         row.StartedAt,
		FinishedAt:        row.FinishedAt,
		RootRunID:         row.Lineage.RootRunID,
		ParentRunID:       parent.String,
		RetryOf:           retryOf,
	}
	if event == runevents.EventFinished {
		facts.Reason = runEventReason(change, row.State)
	}
	body, err := runevents.Build(event, facts)
	if err != nil {
		return err
	}
	id := runevents.ID(row.ID, row.StateSeq)
	for _, sub := range matched {
		if err := insertRunEventDeliveryTx(ctx, tx, team, runEventDelivery{SubscriptionID: sub.ID, EventID: id, RunID: row.ID, EventType: event, Body: body}, now); err != nil {
			return err
		}
	}
	return nil
}

// safety: cancel and host reasons carry caller-written text, so a reason comes only from the
// transition's own cause or the plan failure the store itself records.
func runEventReason(change runstate.RunTransition, state runstate.RunState) string {
	switch change.Cause {
	case runstate.RunQueueTimeout, runstate.RunOwnerLost, runstate.RunLaunchRejected, runCreditRefused:
		return string(change.Cause)
	case runstate.RunFinish:
		if state == runstate.RunFailed && change.Reason == string(apierr.PlanFailed) {
			return change.Reason
		}
	}
	return ""
}
