package teststore

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// SeedTrigger adds a reporting run and its owner projection without admitting executable work.
func SeedTrigger(s *store.Store, ctx context.Context, team store.Team, v store.Trigger) error {
	return SeedTriggerWithRun(s, ctx, team, v, store.Run{ID: v.ID, Pipeline: v.Pipeline, Status: "pending", CreatedAt: v.CreatedAt, Args: v.Args, DeclaredRepo: v.Repo, RepoURL: v.RepoURL, GitBranch: v.GitBranch, GitSHA: v.GitSHA, TriggerSource: v.TriggerSource, ParentRunID: v.ParentRunID, RetryOf: v.RetryOf})
}

// SeedTriggerClaim keeps reporting fixtures from assembling partial owner fences.
func SeedTriggerClaim(s *store.Store, ctx context.Context, team store.Team, run string, actor store.Actor, until time.Time, started *time.Time) error {
	var start any
	if started != nil {
		start = started.UnixNano()
	}
	query := `UPDATE triggers SET status='claimed',claimed_by=?,capability_prefix=?,generation=?,lease_expires_at=?,claimed_at=?,started_at=?,finished_at=NULL WHERE team=? AND id=?`
	if s.Dialect() == store.DialectPostgres {
		query = `UPDATE triggers SET status='claimed',claimed_by=$1,capability_prefix=$2,generation=$3,lease_expires_at=$4,claimed_at=$5,started_at=$6,finished_at=NULL WHERE team=$7 AND id=$8`
	}
	result, err := Exec(ctx, s, query, actor.Holder, actor.CapabilityPrefix, actor.Generation, until.UnixNano(), Now(s).UnixNano(), start, team, run)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return fmt.Errorf("fixture trigger %s/%s: matched %d rows", team, run, n)
	}
	return err
}

// SeedTriggerWithRun adds reporting facts and the corresponding owner queue row.
func SeedTriggerWithRun(s *store.Store, ctx context.Context, team store.Team, v store.Trigger, run store.Run) error {
	if v.ID != run.ID {
		return store.ErrInvalidInput
	}
	if err := SeedRun(s, ctx, team, run); err != nil {
		return err
	}
	status := v.Status
	if status == "" {
		status = "pending"
	}
	now := v.CreatedAt
	if now.IsZero() {
		now = Now(s)
	}
	query := `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,needs_labels,prefers_labels,webhook_delivery,webhook_replay_key) VALUES (?,?,?,'trigger',?,?,?,?,?,?)`
	if s.Dialect() == store.DialectPostgres {
		query = `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,needs_labels,prefers_labels,webhook_delivery,webhook_replay_key) VALUES ($1,$2,$3,'trigger',$4,$5,$6,$7,$8,$9)`
	}
	_, err := Exec(ctx, s, query, team, v.ID, status, now.UnixNano(), now.UnixNano(), []byte(`[]`), []byte(`[]`), v.WebhookDelivery, v.WebhookReplayKey)
	return err
}
