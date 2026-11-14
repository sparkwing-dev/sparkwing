package store

import (
	"context"
	"database/sql"
	"regexp"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runevents"
)

// RunEventDeliveryWindow is how long a delivery is retried before it is marked failed.
const RunEventDeliveryWindow = 24 * time.Hour

// safety: cancel reasons can be caller-supplied prose, so only code-shaped
// reasons reach a receiver.
var runEventReasonCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// safety: runs inside the run transition, so a state change and its deliveries commit or roll back together.
func writeRunEventDeliveriesTx(ctx context.Context, tx *storeTx, team Team, row RunRow, kind, reason string, now time.Time) error {
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
	if event == runevents.EventFinished && runEventReasonCode.MatchString(reason) {
		facts.Reason = reason
	}
	body, err := runevents.Build(event, facts)
	if err != nil {
		return err
	}
	id := runevents.ID(row.ID, row.StateSeq)
	at := now.UnixNano()
	for _, sub := range matched {
		if _, err := tx.execContext(ctx, `INSERT INTO run_event_deliveries(team,subscription_id,event_id,run_id,event_type,body,state,attempts,next_at,deadline_at,created_at) VALUES (?,?,?,?,?,?,'pending',0,?,?,?)`, team, sub.ID, id, row.ID, event, body, at, now.Add(RunEventDeliveryWindow).UnixNano(), at); err != nil {
			return err
		}
	}
	return nil
}
