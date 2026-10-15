package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStoreAbandonedLogBoundary(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "store-abandoned-logs.db")
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	commit, err := seedStoreLogBoundary(ctx, s.DB())
	if err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	segment := store.Segment{Index: 1, Size: 3, Digest: strings.Repeat("a", 64)}
	segment.ObjectKey, err = blob.LogObjectKey(commit.Subject, commit.RetentionClass, 1)
	if err != nil {
		return err
	}
	commit.Segments = []store.Segment{segment}
	if err := tenant.RecordSegments(ctx, commit); err != nil {
		return err
	}
	lease, err := s.AsOperator().AcquireSingletonLease(ctx, "maintenance", "log-boundary", time.Hour)
	if err != nil {
		return err
	}
	if n, err := s.AsOperator().SealAbandonedLogStreams(ctx, lease, 10); err != nil || n != 0 {
		return fmt.Errorf("maintenance sealed a live stream: %d: %v", n, err)
	}
	finished := time.Now().Add(-2 * limits.LogSealGrace).UnixNano()
	if _, err := s.DB().ExecContext(ctx, "UPDATE nodes SET status='done',outcome='success',finished_at=?,claimed_by=NULL,capability_prefix='',lease_expires_at=NULL WHERE team=? AND run_id='logs'", finished, store.DefaultTeam); err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE node_attempts SET finished_at=?,outcome='success' WHERE team=? AND run_id='logs'", finished, store.DefaultTeam); err != nil {
		return err
	}
	before, err := tenant.ReadNode(ctx, store.NodeRef{RunID: "logs", NodeID: "job"})
	if err != nil {
		return err
	}
	reader, err := seedStoreLogReader(ctx, s.DB(), commit.Actor.CapabilityPrefix)
	if err != nil {
		return err
	}
	if n, err := s.AsOperator().SealAbandonedLogStreams(ctx, lease, 10); err != nil || n != 1 {
		return fmt.Errorf("maintenance did not seal an abandoned stream: %d: %v", n, err)
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
	got, err := tenant.ReadLogSegments(ctx, reader, blob.LogReadRequest{Subject: commit.Subject})
	if err != nil || got.Complete || got.IncompleteReason != "writer_lost" || !reflect.DeepEqual(got.Segments, commit.Segments) {
		return fmt.Errorf("reopened abandoned log=%+v: %v", got, err)
	}
	if n, err := s.AsOperator().SealAbandonedLogStreams(ctx, lease, 10); err != nil || n != 0 {
		return fmt.Errorf("abandoned seal replay changed a stream: %d: %v", n, err)
	}
	after, err := tenant.ReadNode(ctx, store.NodeRef{RunID: "logs", NodeID: "job"})
	if err != nil || !reflect.DeepEqual(before, after) {
		return fmt.Errorf("log loss changed the job result: %+v: %v", after, err)
	}
	return nil
}
