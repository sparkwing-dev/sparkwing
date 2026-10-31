package store

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestStoreDataStandingReadsRetainedEventsInsteadOfHistoricalUsage(t *testing.T) {
	s, tenant, _, _ := resultFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE runs SET event_bytes=17 WHERE team='a'")
	rewriteSchemaExec(t, s.db, "INSERT INTO storage_run_usage(team,run_id,period,bytes,objects,updated_at) VALUES ('a','run','1970-01',1000,1,1)")
	got, err := tenant.StorageStanding(t.Context())
	if err != nil || got.EventBytes != 17 || got.Tier != teamTierFree {
		t.Fatalf("standing=%+v, want 17 retained event bytes: %v", got, err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a'")
	got, err = tenant.StorageStanding(t.Context())
	if err != nil || got.EventBytes != 0 {
		t.Fatalf("deleted run still counts retained events: %+v: %v", got, err)
	}
}

func TestStoreDataStandingTracksOnlyLiveUploadHolds(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	check := func(blobs, held int64) {
		t.Helper()
		got, err := tenant.StorageStanding(t.Context())
		if err != nil || got.BlobBytes != blobs || got.ReservedBlobBytes != held {
			t.Fatalf("standing=%+v, want blobs=%d held=%d: %v", got, blobs, held, err)
		}
	}
	first, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	check(0, 3)
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(first, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	check(3, 0)
	request.ID = "duplicate"
	duplicate, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	check(3, 0)
	if err := tenant.AbortBlobUpload(t.Context(), request.Intent.Authority, duplicate.ID); err != nil {
		t.Fatal(err)
	}
	request.ID, request.Intent.Digest = "aborted", blob.Digest(strings.Repeat("b", 64))
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	check(3, 3)
	if err := tenant.AbortBlobUpload(t.Context(), request.Intent.Authority, request.ID); err != nil {
		t.Fatal(err)
	}
	check(6, 0)
	request.ID = "expired"
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	check(6, 3)
	s.clock = func() time.Time { return request.ExpiresAt }
	check(6, 0)
	var held int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM quota_reservations WHERE team='a' AND state='held'").Scan(&held); err != nil || held != 3 {
		t.Fatalf("report released holds: held=%d: %v", held, err)
	}
}

func TestStoreDataStandingFollowsFundingAndHolds(t *testing.T) {
	_, tenant, _, _ := resultFixture(t)
	check := func(want TeamStorageTier) {
		t.Helper()
		got, err := tenant.StorageStanding(t.Context())
		if err != nil || got.Tier != want || got.AllowanceBytes != defaultFreeAllowanceBytes {
			t.Fatalf("standing=%+v, want %s: %v", got, want, err)
		}
	}
	check(teamTierFree)
	if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: MicroCreditsPerCredit, Reference: "funding-fixture", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	check(teamTierFunded)
	if _, err := tenant.HoldByOperator(t.Context(), "operator", "held"); err != nil {
		t.Fatal(err)
	}
	check(teamTierFree)
	if _, err := tenant.ReleaseOperatorHolds(t.Context(), "operator", "released"); err != nil {
		t.Fatal(err)
	}
	check(teamTierFunded)
}

func TestStoreDataStandingRejectsCorruptAndOverflowedInventory(t *testing.T) {
	for _, kind := range []string{"negative_blob", "negative_events", "combined_inventory", "pending_inventory"} {
		t.Run(kind, func(t *testing.T) {
			s, tenant, request := uploadFixture(t)
			switch kind {
			case "negative_events":
				rewriteSchemaExec(t, s.db, "PRAGMA ignore_check_constraints=ON")
				rewriteSchemaExec(t, s.db, "UPDATE runs SET event_bytes=-1 WHERE team='a'")
			case "negative_blob":
				rewriteSchemaExec(t, s.db, "PRAGMA ignore_check_constraints=ON")
				rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,-1,1)", strings.Repeat("a", 64))
			case "combined_inventory":
				rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,?,1),('a',?,1,1)", strings.Repeat("a", 64), int64(math.MaxInt64), strings.Repeat("b", 64))
			case "pending_inventory":
				if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,?,1)", strings.Repeat("b", 64), int64(math.MaxInt64))
			}
			got, err := tenant.StorageStanding(t.Context())
			if err == nil || got != (StorageStanding{}) {
				t.Fatalf("corrupt report=%+v: %v", got, err)
			}
		})
	}
}

func FuzzStorageStandingTenantInventory(f *testing.F) {
	f.Add(uint16(3), uint16(7), uint16(17))
	f.Add(uint16(0), uint16(0), uint16(0))
	f.Fuzz(func(t *testing.T, own, foreign, events uint16) {
		s, tenant, actor, attempt := resultFixture(t)
		rewriteSchemaExec(t, s.db, "UPDATE runs SET event_bytes=? WHERE team='a'", int64(events))
		rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,?,1),('b',?,?,1)", strings.Repeat("a", 64), int64(own), strings.Repeat("a", 64), int64(foreign))
		commit := logCommit(actor, attempt)
		commit.Segments[0].Size = int64(own) + 1
		commit.Segments[0].StoredBytes = int64(own) + 1
		if err := tenant.RecordSegments(t.Context(), commit); err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, s.db, "INSERT INTO team_storage(team,store,storage_charged_through,updated_at) VALUES ('a','cache',999999,1),('b','logs',999999,1)")
		rewriteSchemaExec(t, s.db, "INSERT INTO storage_month_usage(team,period,bytes,objects,updated_at) VALUES ('a','1969-12',?,1,1),('b','1969-12',?,1,1)", int64(foreign), int64(own))
		first, err := tenant.StorageStanding(t.Context())
		if err != nil || first.BlobBytes != int64(own) || first.LogBytes != int64(own)+1 || first.EventBytes != int64(events) || first.ReservedBlobBytes != 0 {
			t.Fatalf("tenant inventory=%+v: %v", first, err)
		}
		other, err := (&Tenant{s: s, team: "b"}).StorageStanding(t.Context())
		if err != nil || other.BlobBytes != int64(foreign) || other.LogBytes != 0 || other.EventBytes != 0 || other.ReservedBlobBytes != 0 {
			t.Fatalf("foreign inventory=%+v: %v", other, err)
		}
		path := blobAccountingDiskStore(t, s)
		if err := s.db.Close(); err != nil {
			t.Fatal(err)
		}
		s.db = blobAccountingDB(t, path)
		reopened, err := tenant.StorageStanding(t.Context())
		if err != nil || !reflect.DeepEqual(first, reopened) {
			t.Fatalf("reopened inventory=%+v, want %+v: %v", reopened, first, err)
		}
		var raw, usage int64
		err = errors.Join(s.db.QueryRow("SELECT storage_charged_through FROM team_storage WHERE team='a' AND store='cache'").Scan(&raw), s.db.QueryRow("SELECT bytes FROM storage_month_usage WHERE team='a' AND period='1969-12'").Scan(&usage))
		if err != nil || raw != 999999 || usage != int64(foreign) {
			t.Fatalf("report changed accounting: raw=%d usage=%d: %v", raw, usage, err)
		}
		rewriteSchemaCount(t, s.db, "credit_charges", 0)
	})
}
