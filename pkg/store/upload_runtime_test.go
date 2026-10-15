package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func uploadFixture(t *testing.T) (*Store, *Tenant, UploadReservationRequest) {
	t.Helper()
	s, tenant, actor, attempt := resultFixture(t)
	rewriteSchemaExec(t, s.db, "INSERT INTO storage_quotas(team,max_bytes_per_month,updated_at) VALUES ('a',100,1)")
	return s, tenant, UploadReservationRequest{ID: "ticket_a", Intent: blobIntent(actor, attempt), ExpiresAt: s.now().Add(time.Hour)}
}

func verifiedUpload(ticket UploadReservation, authority blob.Authority) VerifiedUploadCommit {
	return VerifiedUploadCommit{Authority: authority, TicketID: ticket.ID, Verified: ticket.Blob, ReplicaID: "replica-a"}
}

func TestBlobUploadReservationReplayAndCommit(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Identity != request.Intent.Identity || ticket.Blob.Team != "a" || ticket.Blob.Digest != request.Intent.Digest || ticket.Blob.Size != 3 || ticket.Provenance != blob.ProvenanceLocal || ticket.StagingKey != "teams/a/pending/ticket_a" || ticket.ObjectKey != "teams/a/cas/"+string(request.Intent.Digest) || !ticket.ExpiresAt.Equal(request.ExpiresAt) || ticket.CommittedAt != nil {
		t.Fatalf("ticket=%+v", ticket)
	}
	for range 2 {
		replayed, err := tenant.ReserveBlobUpload(t.Context(), request)
		if err != nil || !reflect.DeepEqual(ticket, replayed) {
			t.Fatalf("reservation replay=%+v: %v", replayed, err)
		}
	}
	assertBlobUsage(t, s, 0, 3, 0, 0)
	rewriteSchemaCount(t, s.db, "uploads", 1)
	rewriteSchemaCount(t, s.db, "blob_receipts", 0)
	var first blob.Receipt
	for i := 0; i < 2; i++ {
		receipt, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = receipt
		} else if receipt != first {
			t.Fatal("receipt changed on replay")
		}
	}
	if first.Identity != request.Intent.Identity {
		t.Fatalf("receipt=%+v", first)
	}
	got, err := tenant.LookupBlobUpload(t.Context(), request.Intent.Authority, ticket.ID)
	if err != nil || got.CommittedAt == nil {
		t.Fatalf("committed lookup=%+v: %v", got, err)
	}
	assertBlobUsage(t, s, 3, 0, 3, 1)
}

func TestBlobUploadConflictingReservationIsAtomic(t *testing.T) {
	for _, problem := range []string{"digest", "size", "expiry", "identity", "principal", "upload_hint", "path", "expired"} {
		t.Run(problem, func(t *testing.T) {
			s, tenant, request := uploadFixture(t)
			if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			switch problem {
			case "digest":
				request.Intent.Digest = blob.Digest(strings.Repeat("c", 64))
			case "size":
				request.Intent.Size++
			case "expiry":
				request.ExpiresAt = request.ExpiresAt.Add(time.Second)
			case "identity":
				request.Intent.Identity.Attempt.Value.Attempt++
			case "principal":
				request.Intent.Authority.Principal = "other"
			case "upload_hint":
				request.Intent.Authority.Upload.Set = true
				request.Intent.Authority.Upload.Value = request.Intent.Identity
				request.Intent.Authority.Upload.Value.Principal = "other"
			case "path":
				request.ID = "../ticket_a"
			case "expired":
				request.ExpiresAt = s.now()
			}
			if _, err := tenant.ReserveBlobUpload(t.Context(), request); err == nil {
				t.Fatal("invalid reservation accepted")
			}
			assertBlobUsage(t, s, 0, 3, 0, 0)
			rewriteSchemaCount(t, s.db, "uploads", 1)
		})
	}
}

func TestBlobUploadCommitRechecksTicketAndAuthority(t *testing.T) {
	for _, problem := range []string{"digest", "size", "team", "ticket", "replica", "revoked", "generation", "attempt", "holder", "foreign_uploader", "expired"} {
		t.Run(problem, func(t *testing.T) {
			s, tenant, request := uploadFixture(t)
			ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			commit := verifiedUpload(ticket, request.Intent.Authority)
			switch problem {
			case "digest":
				commit.Verified.Digest = blob.Digest(strings.Repeat("c", 64))
			case "size":
				commit.Verified.Size++
			case "team":
				commit.Verified.Team = "b"
			case "ticket":
				commit.TicketID = "absent"
			case "replica":
				commit.ReplicaID = ""
			case "revoked":
				rewriteSchemaExec(t, s.db, "UPDATE capabilities SET revoked_at=1 WHERE prefix='machine'")
			case "generation":
				rewriteSchemaExec(t, s.db, "UPDATE nodes SET generation=generation+1 WHERE team='a'")
			case "attempt":
				rewriteSchemaExec(t, s.db, "UPDATE nodes SET attempt=attempt+1 WHERE team='a'")
			case "holder":
				rewriteSchemaExec(t, s.db, "UPDATE nodes SET claimed_by='other' WHERE team='a'")
			case "foreign_uploader":
				commit.Authority = addBlobJob(t, s, request.Intent, "unrelated").Authority
			case "expired":
				s.clock = func() time.Time { return request.ExpiresAt }
			}
			if _, err := tenant.CommitBlobUpload(t.Context(), commit); err == nil {
				t.Fatal("invalid commit accepted")
			}
			rewriteSchemaCount(t, s.db, "blobs", 0)
			rewriteSchemaCount(t, s.db, "blob_receipts", 0)
			assertBlobUsage(t, s, 0, 3, 0, 0)
		})
	}
}

func TestBlobUploadCommitAndAbortRollback(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "CREATE TRIGGER fail_ticket_commit BEFORE UPDATE OF committed_at ON uploads BEGIN SELECT RAISE(ABORT,'injected upload failure'); END")
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err == nil {
		t.Fatal("ticket failure ignored")
	}
	rewriteSchemaCount(t, s.db, "blobs", 0)
	rewriteSchemaCount(t, s.db, "blob_receipts", 0)
	assertBlobUsage(t, s, 0, 3, 0, 0)
	rewriteSchemaExec(t, s.db, "DROP TRIGGER fail_ticket_commit")
	for range 2 {
		if err := tenant.AbortBlobUpload(t.Context(), request.Intent.Authority, ticket.ID); err != nil {
			t.Fatal(err)
		}
	}
	assertBlobUsage(t, s, 0, 0, 0, 0)
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err == nil {
		t.Fatal("aborted ticket committed")
	}
	if _, err := tenant.LookupBlobUpload(t.Context(), request.Intent.Authority, ticket.ID); err == nil {
		t.Fatal("aborted ticket signed")
	}
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err == nil {
		t.Fatal("aborted ticket reopened")
	}
}

func TestBlobUploadQuotaAdmissionAndReservationRollback(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE storage_quotas SET max_bytes_per_month=4 WHERE team='a'")
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.ID = "ticket_b"
	var refused *StorageQuotaError
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.As(err, &refused) {
		t.Fatalf("quota refusal=%v", err)
	}
	assertBlobUsage(t, s, 0, 3, 0, 0)
	rewriteSchemaCount(t, s.db, "uploads", 1)
	if err := tenant.AbortBlobUpload(t.Context(), request.Intent.Authority, "ticket_a"); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "CREATE TRIGGER fail_ticket_reserve BEFORE INSERT ON uploads BEGIN SELECT RAISE(ABORT,'injected reservation failure'); END")
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err == nil {
		t.Fatal("partial reservation accepted")
	}
	assertBlobUsage(t, s, 0, 0, 0, 0)
	rewriteSchemaExec(t, s.db, "DROP TRIGGER fail_ticket_reserve")
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestBlobUploadDurableReplayAndDeduplication(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	path := blobAccountingDiskStore(t, s)
	ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	other := addBlobJob(t, s, request.Intent, "other")
	request.ID, request.Intent = "ticket_b", other
	second, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	commit := verifiedUpload(second, other.Authority)
	commit.ReplicaID = "replica-b"
	if _, err := tenant.CommitBlobUpload(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	for range 2 {
		if _, err := tenant.CommitBlobUpload(t.Context(), commit); err != nil {
			t.Fatal(err)
		}
	}
	if err := tenant.AbortBlobUpload(t.Context(), other.Authority, second.ID); err != nil {
		t.Fatal(err)
	}
	assertBlobUsage(t, s, 3, 0, 3, 1)
	rewriteSchemaCount(t, s.db, "blob_receipts", 2)
}

func TestBlobUploadAuthorizedDeduplicationAtQuotaCeiling(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE storage_quotas SET max_bytes_per_month=3 WHERE team='a'")
	ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	request.ID = "duplicate"
	duplicate, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatalf("authorized duplicate refused at ceiling: %v", err)
	}
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(duplicate, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	request.ID = "new_bytes"
	request.Intent.Digest = blob.Digest(strings.Repeat("c", 64))
	var refusal *StorageQuotaError
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.As(err, &refusal) {
		t.Fatalf("new bytes at ceiling: %v", err)
	}
	request.ID = "unreadable_duplicate"
	request.Intent = addBlobJob(t, s, ticketIntent(ticket, request.Intent.Authority), "unrelated")
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); !errors.As(err, &refusal) {
		t.Fatalf("unreadable bytes revealed through quota admission: %v", err)
	}
	assertBlobUsage(t, s, 3, 0, 3, 1)
}

func TestBlobUploadDeduplicationCannotFundMissingBytes(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	request.ID = "deduplicated"
	duplicate, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM blobs WHERE team='a' AND digest=?", request.Intent.Digest)
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(duplicate, request.Intent.Authority)); err == nil {
		t.Fatal("zero reservation minted new content")
	}
	rewriteSchemaCount(t, s.db, "blobs", 0)
	rewriteSchemaCount(t, s.db, "blob_receipts", 0)
	assertBlobUsage(t, s, 3, 0, 3, 1)
}

func ticketIntent(u UploadReservation, authority blob.Authority) blob.UploadIntent {
	return blob.UploadIntent{Authority: authority, Identity: u.Identity, Digest: u.Blob.Digest, Size: u.Blob.Size}
}

func TestBlobUploadIndependentClientsRaceAndReplay(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	path := blobAccountingDiskStore(t, s)
	other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}
	otherTenant := &Tenant{s: other, team: "a"}
	clients := []*Tenant{tenant, otherTenant}
	start := make(chan struct{})
	type result struct {
		ticket UploadReservation
		err    error
	}
	results := make(chan result, 2)
	for _, client := range clients {
		go func() {
			<-start
			ticket, err := client.ReserveBlobUpload(t.Context(), request)
			results <- result{ticket, err}
		}()
	}
	close(start)
	var ticket UploadReservation
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		ticket = got.ticket
	}
	rewriteSchemaCount(t, s.db, "uploads", 1)
	assertBlobUsage(t, s, 0, 3, 0, 0)
	start = make(chan struct{})
	for _, client := range clients {
		go func() {
			<-start
			_, err := client.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority))
			results <- result{err: err}
		}()
	}
	close(start)
	for range 2 {
		if got := <-results; got.err != nil {
			t.Fatal(got.err)
		}
	}
	assertBlobUsage(t, s, 3, 0, 3, 1)
	rewriteSchemaCount(t, s.db, "blob_receipts", 1)
}

func TestBlobUploadExpiredReservationFreesCapacity(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE storage_quotas SET max_bytes_per_month=3 WHERE team='a'")
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	now := request.ExpiresAt
	s.clock = func() time.Time { return now }
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET lease_expires_at=? WHERE team='a'", request.ExpiresAt.Add(time.Hour).UnixNano())
	request.ID = "new_ticket"
	request.ExpiresAt = request.ExpiresAt.Add(time.Minute)
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	assertBlobUsage(t, s, 0, 3, 0, 0)
	if err := tenant.AbortBlobUpload(t.Context(), request.Intent.Authority, "ticket_a"); err != nil {
		t.Fatal(err)
	}
	assertBlobUsage(t, s, 0, 3, 0, 0)
}

func TestBlobUploadEmptyCommitAndExpiredReplay(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	request.Intent.Size = 0
	ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE quota_reservations SET expires_at=1 WHERE team='a'")
	rewriteSchemaExec(t, s.db, "UPDATE uploads SET expires_at=1 WHERE team='a'")
	if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	assertBlobUsage(t, s, 0, 0, 0, 1)
}

func TestBlobUploadLeasedGCReleasesExpiredTicketsAfterRunDeletion(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.ID = "orphan"
	if _, err := tenant.ReserveBlobUpload(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
	rewriteSchemaCount(t, s.db, "uploads", 0)
	now := request.ExpiresAt
	s.clock = func() time.Time { return now }
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "replica", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	stale := lease
	stale.Generation++
	if _, err := s.AsOperator().MarkBlobTombstones(t.Context(), stale, 1); err == nil {
		t.Fatal("stale maintenance released quota")
	}
	assertBlobUsage(t, s, 0, 6, 0, 0)
	if _, err := s.AsOperator().MarkBlobTombstones(t.Context(), lease, 1); err != nil {
		t.Fatal(err)
	}
	assertBlobUsage(t, s, 0, 3, 0, 0)
	for range 2 {
		if _, err := s.AsOperator().MarkBlobTombstones(t.Context(), lease, 1); err != nil {
			t.Fatal(err)
		}
	}
	assertBlobUsage(t, s, 0, 0, 0, 0)
}

func TestBlobUploadCompetesWithGenericQuotaReservation(t *testing.T) {
	s, tenant, request := uploadFixture(t)
	request.Intent.Size = 8
	path := blobAccountingDiskStore(t, s)
	other := &Store{db: blobAccountingDB(t, path), dialect: DialectSQLite, clock: s.clock}
	otherTenant := &Tenant{s: other, team: "a"}
	rewriteSchemaExec(t, s.db, "INSERT INTO quota_admission(team,quota,period,used,updated_at) VALUES ('a','storage',?,90,1)", s.now().Format("2006-01"))
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; _, err := tenant.ReserveBlobUpload(t.Context(), request); results <- err }()
	go func() { <-start; _, err := otherTenant.ReserveQuota(t.Context(), "storage", 8); results <- err }()
	close(start)
	winners := 0
	for range 2 {
		if err := <-results; err == nil {
			winners++
		} else if !errors.Is(err, ErrStorageQuota) {
			t.Error(err)
		}
	}
	if winners != 1 {
		t.Fatalf("quota race winners=%d", winners)
	}
	assertBlobUsage(t, s, 90, 8, 0, 0)
}

func TestBlobUploadOwnerTicketsRetainUnitIdentity(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan} {
		t.Run(string(kind), func(t *testing.T) {
			s, claim := releaseFixture(t, kind)
			tenant := &Tenant{s: s, team: "a"}
			unit := blob.UploadUnit{Kind: blob.UploadUnitKind(kind), ID: "run", Generation: 7}
			identity := blob.UploadIdentity{Kind: blob.UploadOwnerUnit, Team: "a", RunID: "run", Principal: "machine", CapabilityRef: claim.Award.CapabilityPrefix, Unit: runspec.Optional[blob.UploadUnit]{Set: true, Value: unit}}
			intent := blob.UploadIntent{Identity: identity, Authority: blob.Authority{CapabilityRef: identity.CapabilityRef, Upload: runspec.Optional[blob.UploadIdentity]{Set: true, Value: identity}}, Digest: blob.Digest(strings.Repeat("a", 64)), Size: 3}
			if _, err := commitTestBlob(t, tenant, intent); err != nil {
				t.Fatal(err)
			}
			var tagged string
			var generation int64
			if err := s.db.QueryRow("SELECT uploader_kind,unit_generation FROM uploads WHERE team='a'").Scan(&tagged, &generation); err != nil || tagged != "owner_unit" || generation != 7 {
				t.Fatalf("owner ticket=%s,%d: %v", tagged, generation, err)
			}
			intent.Identity.Unit.Value.Generation++
			if _, err := commitTestBlob(t, tenant, intent); err == nil {
				t.Fatal("owner upload changed its unit fence")
			}
			rewriteSchemaCount(t, s.db, "blob_receipts", 1)
		})
	}
}

func TestBlobUploadPublicationTicketsKeepImportedAttempt(t *testing.T) {
	for _, imported := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "imported_attempt"}[imported], func(t *testing.T) {
			s, tenant, actor, attempt := resultFixture(t)
			root := authorityRoot("publisher-root", "a")
			root.Subject.ID = "machine"
			root.Grants = []capability.Grant{capability.DataRead, capability.DataWrite}
			authorityPersist(t, s, root)
			var publisher capability.Capability
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				var err error
				publisher, err = loadCapabilityTx(t.Context(), tx, actor.CapabilityPrefix)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			publisher.Prefix = "publisher"
			publisher.Digest = [32]byte{1}
			publisher.Issuance = capability.Delegate
			publisher.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
			publisher.Unit, publisher.UnitID, publisher.Purpose, publisher.Delivery = "", "", "", ""
			publisher.Billing = nil
			publisher.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "run"}
			publisher.Data.Use = capability.DataPublish
			authorityPersist(t, s, publisher)
			rewriteSchemaExec(t, s.db, "UPDATE runs SET intake_mode='publish' WHERE team='a' AND id='run'")
			identity := blob.UploadIdentity{Kind: blob.UploadPublication, Team: "a", RunID: "run", Principal: "machine", CapabilityRef: publisher.Prefix}
			if imported {
				rewriteSchemaExec(t, s.db, "UPDATE node_attempts SET finished_at=2,outcome='success' WHERE team='a' AND run_id='run'")
				identity.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}
			}
			intent := blob.UploadIntent{Identity: identity, Authority: blob.Authority{CapabilityRef: publisher.Prefix, Attempt: identity.Attempt}, Digest: blob.Digest(strings.Repeat("a", 64)), Size: 3}
			receipt, err := commitTestBlob(t, tenant, intent)
			if err != nil || receipt.Identity != identity {
				t.Fatalf("publication receipt=%+v: %v", receipt, err)
			}
			var provenance string
			if err := s.db.QueryRow("SELECT provenance FROM uploads WHERE team='a'").Scan(&provenance); err != nil || provenance != "local" {
				t.Fatalf("publication provenance=%q: %v", provenance, err)
			}
			if !imported {
				records := &blobRecords{tenant: tenant, rules: fakeBlobRules{}}
				request := blobOutputRequest(blobIntent(actor, attempt))
				request.Authority, request.Source = intent.Authority, identity
				if _, err := records.CommitRecord(t.Context(), request); err == nil {
					t.Fatal("run publication manufactured job output")
				}
			}
		})
	}
}
