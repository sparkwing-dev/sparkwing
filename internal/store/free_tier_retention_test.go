package store

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestFreeSlotsReleaseAfterThirtyEmptyDays(t *testing.T) {
	s, _, now := maintenanceStore(t)
	ctx := context.Background()
	op := s.AsOperator()
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('aaa',1,1),('bbb',1,1)`)
	for _, team := range []Team{"aaa", "bbb"} {
		if err := s.GrantFreeSlot(ctx, team); err != nil {
			t.Fatal(err)
		}
	}
	maintenanceRun(t, s, "busy", 1, runstate.RunSuccess)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET team='bbb' WHERE id='busy'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO blobs(team,digest,size,created_at) VALUES ('bbb','stored',3,?)`, now.UnixNano())
	lease, err := op.AcquireSingletonLease(ctx, "maintenance", "free-slots", 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	check := func(wantReleased, wantTaken int64) {
		t.Helper()
		result, err := op.MaintainRetention(ctx, RetentionRequest{Lease: lease, Limit: 100})
		if err != nil || result.FreeSlotsReleased != wantReleased {
			t.Fatalf("retention at %s: %+v %v", now.Format(time.RFC3339), result, err)
		}
		taken, _, err := op.FreeSlots(ctx)
		if err != nil || taken != wantTaken {
			t.Fatalf("slots at %s: %d %v", now.Format(time.RFC3339), taken, err)
		}
	}
	check(0, 2)
	*now = now.Add(29 * 24 * time.Hour)
	check(0, 2)
	*now = now.Add(24 * time.Hour)
	check(1, 1)
	rewriteSchemaExec(t, s.db, `DELETE FROM runs WHERE team='bbb'`)
	*now = now.Add(24 * time.Hour)
	check(0, 1)
	rewriteSchemaExec(t, s.db, `DELETE FROM blobs WHERE team='bbb'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO source_uploads(team,id,authority_kind,authority_id,repo_key,repo_display,execution_sha,pipeline_sha,digest,size,expires_at)
		VALUES ('bbb','pending','capability','owner','gh:1','acme/repo','sha','sha','digest',3,?)`, now.Add(-24*time.Hour).UnixNano())
	*now = now.Add(24 * time.Hour)
	check(0, 1)
	rewriteSchemaExec(t, s.db, `DELETE FROM source_uploads WHERE team='bbb' AND id='pending'`)
	*now = now.Add(24 * time.Hour)
	check(0, 1)
	*now = now.Add(29 * 24 * time.Hour)
	check(0, 1)
	*now = now.Add(24 * time.Hour)
	check(1, 0)
}

func TestFreeSlotIdleClockRestartsAfterTransientStoredBytes(t *testing.T) {
	s, _, now := maintenanceStore(t)
	ctx := context.Background()
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('aaa',1,1)`)
	if err := s.GrantFreeSlot(ctx, "aaa"); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AsOperator().AcquireSingletonLease(ctx, "maintenance", "free-slots", 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().MaintainRetention(ctx, RetentionRequest{Lease: lease, Limit: 100}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(29 * 24 * time.Hour)
	if err := s.inTx(ctx, func(tx *storeTx) error {
		_, err := commitBlobBytesTx(ctx, tx, "aaa", blob.Blob{Digest: "brief", Size: 3}, 3)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `DELETE FROM blobs WHERE team='aaa' AND digest='brief'`)
	*now = now.Add(24 * time.Hour)
	result, err := s.AsOperator().MaintainRetention(ctx, RetentionRequest{Lease: lease, Limit: 100})
	if err != nil || result.FreeSlotsReleased != 0 {
		t.Fatalf("transient bytes lost idle clock: %+v %v", result, err)
	}
	*now = now.Add(29 * 24 * time.Hour)
	result, err = s.AsOperator().MaintainRetention(ctx, RetentionRequest{Lease: lease, Limit: 100})
	if err != nil || result.FreeSlotsReleased != 0 {
		t.Fatalf("released before thirty empty days: %+v %v", result, err)
	}
	*now = now.Add(24 * time.Hour)
	result, err = s.AsOperator().MaintainRetention(ctx, RetentionRequest{Lease: lease, Limit: 100})
	if err != nil || result.FreeSlotsReleased != 1 {
		t.Fatalf("failed to release after thirty empty days: %+v %v", result, err)
	}
}
