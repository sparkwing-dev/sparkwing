package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStorePublishedLogBoundary(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "published-logs.db")
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	request, err := seedStorePresenceAttempts(ctx, s.DB())
	if err != nil {
		return err
	}
	for _, query := range []string{
		"UPDATE runs SET status='success',finished_at=2 WHERE team=? AND id='upload'",
		"UPDATE nodes SET status='done',outcome='success',finished_at=2 WHERE team=? AND run_id='upload'",
		"UPDATE node_attempts SET finished_at=2,outcome='success' WHERE team=? AND run_id='upload'",
	} {
		if _, err := s.DB().ExecContext(ctx, query, store.DefaultTeam); err != nil {
			return err
		}
	}
	actor := store.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: request.Intent.Authority.CapabilityRef}, CapabilityPrefix: request.Intent.Authority.CapabilityRef}
	attempt := runspec.AttemptIdentity{Team: string(store.DefaultTeam), RunID: "upload", NodeID: "a", Attempt: 1, Generation: 7}
	commit := store.SegmentCommit{Subject: blob.LogSubject{Kind: blob.LogJobAttempt, Team: attempt.Team, RunID: attempt.RunID, Attempt: &attempt}, Actor: actor, RetentionClass: "r30", Final: true, IncompleteReason: "local tail unavailable"}
	segment := store.Segment{Index: 1, Size: 3, StoredBytes: 3, Digest: strings.Repeat("a", 64)}
	segment.ObjectKey, err = blob.LogObjectKey(commit.Subject, commit.RetentionClass, 1)
	if err != nil {
		return err
	}
	commit.Segments = []store.Segment{segment}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	for range 2 {
		if err := tenant.RecordSegments(ctx, commit); err != nil {
			return fmt.Errorf("publish completed local log: %w", err)
		}
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := store.Open(path)
	if err != nil {
		return err
	}
	s = reopened
	tenant, err = s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	reader, err := seedStoreLogReader(ctx, s.DB(), actor.CapabilityPrefix)
	if err != nil {
		return err
	}
	got, err := tenant.ReadLogSegments(ctx, reader, blob.LogReadRequest{Subject: commit.Subject, Offset: 1})
	if err != nil || got.Complete || got.IncompleteReason != commit.IncompleteReason || !reflect.DeepEqual(got.Segments, commit.Segments) {
		return fmt.Errorf("reopened published log=%+v: %v", got, err)
	}
	bytes, err := tenant.StorageRetainedBytes(ctx)
	if err != nil || bytes != 3 {
		return fmt.Errorf("published retained bytes=%d, want 3: %v", bytes, err)
	}
	var outcome string
	if err := s.DB().QueryRowContext(ctx, "SELECT outcome FROM nodes WHERE team=? AND run_id='upload' AND node_id='a'", store.DefaultTeam).Scan(&outcome); err != nil || outcome != "success" {
		return fmt.Errorf("log completeness changed local result: %s: %v", outcome, err)
	}
	for _, table := range []string{"triggers", "eval_units", "billing_awards", "billable_intervals"} {
		var count int
		if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE team=?", store.DefaultTeam).Scan(&count); err != nil || count != 0 {
			return fmt.Errorf("publication manufactured %s rows: %d: %v", table, count, err)
		}
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE capabilities SET revoked_at=1 WHERE prefix=?", actor.CapabilityPrefix); err != nil {
		return err
	}
	if err := tenant.RecordSegments(ctx, commit); err == nil {
		return errors.New("revoked publisher replayed final log commit")
	}
	return nil
}
