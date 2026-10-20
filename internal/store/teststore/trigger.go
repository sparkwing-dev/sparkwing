package teststore

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// SeedTrigger adds a reporting run and its owner projection without admitting executable work.
func SeedTrigger(s *store.Store, ctx context.Context, team store.Team, v store.Trigger) error {
	return SeedTriggerWithRun(s, ctx, team, v, store.Run{ID: v.ID, Pipeline: v.Pipeline, Status: "pending", CreatedAt: v.CreatedAt, Args: v.Args, DeclaredRepo: v.Repo, RepoURL: v.RepoURL, GitBranch: v.GitBranch, GitSHA: v.GitSHA, TriggerSource: v.TriggerSource, ParentRunID: v.ParentRunID, RetryOf: v.RetryOf})
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
		now = time.Now().UTC()
	}
	query := `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,needs_labels,prefers_labels,webhook_delivery,webhook_replay_key) VALUES (?,?,?,'trigger',?,?,?,?,?,?)`
	if s.Dialect() == store.DialectPostgres {
		query = `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,needs_labels,prefers_labels,webhook_delivery,webhook_replay_key) VALUES ($1,$2,$3,'trigger',$4,$5,$6,$7,$8,$9)`
	}
	_, err := Exec(ctx, s, query, team, v.ID, status, now.UnixNano(), now.UnixNano(), []byte(`[]`), []byte(`[]`), v.WebhookDelivery, v.WebhookReplayKey)
	return err
}
