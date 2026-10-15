package store

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestBlobOwnerReceiptCannotTransferToAJob(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan} {
		t.Run(string(kind), func(t *testing.T) {
			s, claim := releaseFixture(t, kind)
			tenant := &Tenant{s: s, team: "a"}
			path := blobAccountingDiskStore(t, s)
			identity := blob.UploadIdentity{Kind: blob.UploadOwnerUnit, Team: "a", RunID: "run", Principal: "machine", CapabilityRef: claim.Award.CapabilityPrefix, Unit: runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadUnitKind(kind), ID: "run", Generation: 7}}}
			request := UploadReservationRequest{ID: "owner", Intent: blob.UploadIntent{Authority: blob.Authority{CapabilityRef: identity.CapabilityRef}, Identity: identity, Digest: blob.Digest(strings.Repeat("a", 64)), Size: 3}, ExpiresAt: s.now().Add(time.Hour)}
			ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if receipt, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err != nil || receipt.Identity != identity {
				t.Fatalf("owner receipt=%+v: %v", receipt, err)
			}
			if err := s.db.Close(); err != nil {
				t.Fatal(err)
			}
			s.db = blobAccountingDB(t, path)
			var uploader string
			var job, attempt any
			if err := s.db.QueryRow("SELECT uploader_kind,node_id,attempt FROM blob_receipts WHERE team='a'").Scan(&uploader, &job, &attempt); err != nil || uploader != "owner_unit" || job != nil || attempt != nil {
				t.Fatalf("owner receipt acquired a synthetic job: %s %v %v: %v", uploader, job, attempt, err)
			}
			property := func(n uint32, field uint8) bool {
				forged := request
				forged.ID = fmt.Sprintf("forged_%d_%d", n, field)
				switch field % 6 {
				case 0:
					forged.Intent.Identity.Principal += blob.Principal(fmt.Sprint(n))
				case 1:
					forged.Intent.Identity.Team += fmt.Sprint(n)
				case 2:
					forged.Intent.Identity.RunID += fmt.Sprint(n)
				case 3:
					forged.Intent.Identity.CapabilityRef += fmt.Sprint(n)
				case 4:
					forged.Intent.Identity.Unit.Value.Generation += 1 + int64(n)
				case 5:
					forged.Intent.Identity.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 1 + int64(n), Generation: 7}}
				}
				_, err := tenant.ReserveBlobUpload(t.Context(), forged)
				return err != nil
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 96, Rand: rand.New(rand.NewSource(23))}); err != nil {
				t.Fatal(err)
			}
			unit, err := encodeAwardUnit(UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
			if err != nil {
				t.Fatal(err)
			}
			var issued capability.Issued
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				var err error
				issued, err = s.Issue(t.Context(), tx, capability.IssueRequest{Kind: capability.Award, Award: &capability.AwardSpec{Claimant: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "machine"}, Unit: capability.UnitNode, UnitID: unit, Purpose: capability.PurposeWork, Delivery: capability.DeliveryDirect}})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE nodes SET capability_prefix=? WHERE team='a' AND run_id='run' AND node_id='job'", issued.Capability.Prefix)
			rewriteSchemaExec(t, s.db, "INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES ('a','run','job',2,7,'machine',?,'runner','machine','remote',?)", issued.Capability.Prefix, s.now().UnixNano())
			authority := blob.Authority{CapabilityRef: issued.Capability.Prefix}
			presence, err := tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: authority, Digests: []blob.Digest{ticket.Blob.Digest}})
			if err != nil || len(presence.Present) != 0 {
				t.Fatalf("job inherited its owner's receipt: %+v: %v", presence, err)
			}
			authorized := capability.Authorized{Principal: capability.Principal{Capability: issued.Capability}}
			for _, operation := range []capability.DataOperation{capability.DataDownload, capability.DataRecord} {
				if err := tenant.CheckDataAccess(t.Context(), authorized, capability.DataTarget{Operation: operation, Digest: string(ticket.Blob.Digest)}); !errors.Is(err, blob.ErrDigestNotReadable) {
					t.Fatalf("job inherited owner authority through %s: %v", operation, err)
				}
			}
			rewriteSchemaCount(t, s.db, "blob_receipts", 1)
			rewriteSchemaCount(t, s.db, "uploads", 1)
		})
	}
}
