package store

import (
	"encoding/json"
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

func evalUploadFixture(t *testing.T) (*Store, *Tenant, UploadReservationRequest) {
	t.Helper()
	s, claim := releaseFixture(t, capability.UnitEval)
	// safety: eval upload authority is a contract fixture; issued eval awards grant only data.read.
	var raw []byte
	if err := s.db.QueryRow("SELECT authority_json FROM capabilities WHERE prefix=?", claim.Award.CapabilityPrefix).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record CapabilityRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record.Value.Grants = append(record.Value.Grants, capability.DataWrite)
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := json.Marshal(record.Value.Grants)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE capabilities SET authority_json=?,grants=? WHERE prefix=?", raw, grants, claim.Award.CapabilityPrefix)
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET generation=2,status='done',outcome='success',finished_at=1,claimed_by=NULL,lease_expires_at=NULL,capability_prefix='' WHERE team='a' AND run_id='run'")
	id, err := encodeAwardUnit(UnitRef{Kind: capability.UnitEval, RunID: "run", NodeID: "job", SourceGeneration: 2})
	if err != nil {
		t.Fatal(err)
	}
	source := runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 2, Generation: 2}
	identity := blob.UploadIdentity{Kind: blob.UploadOwnerUnit, Team: "a", RunID: "run", Principal: "machine", CapabilityRef: claim.Award.CapabilityPrefix, Unit: runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadUnitKind(capability.UnitEval), ID: id, Generation: 7, Source: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: source}}}}
	request := UploadReservationRequest{ID: "eval", Intent: blob.UploadIntent{Authority: blob.Authority{CapabilityRef: identity.CapabilityRef}, Identity: identity, Digest: blob.Digest(strings.Repeat("a", 64)), Size: 3}, ExpiresAt: s.now().Add(time.Hour)}
	return s, &Tenant{s: s, team: "a"}, request
}

func TestBlobEvalReceiptBindsBothUnitAndSourceFences(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.3s measured in the short class; the full suite runs it")
	}
	s, tenant, request := evalUploadFixture(t)
	path := blobAccountingDiskStore(t, s)
	ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err != nil || receipt.Identity != request.Intent.Identity {
		t.Fatalf("eval receipt=%+v: %v", receipt, err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = blobAccountingDB(t, path)
	if got, err := tenant.LookupBlobUpload(t.Context(), request.Intent.Authority, ticket.ID); err != nil || got.Identity != request.Intent.Identity || got.Identity.Attempt.Set {
		t.Fatalf("reopened eval ticket=%+v: %v", got, err)
	}
	property := func(n uint32, field uint8) bool {
		forged := request
		forged.ID = fmt.Sprintf("forged_%d_%d", n, field)
		unit := &forged.Intent.Identity.Unit.Value
		switch field % 8 {
		case 0:
			unit.Generation += 1 + int64(n)
		case 1:
			unit.ID += fmt.Sprint(n)
		case 2:
			unit.Source.Value.Generation += 1 + int64(n)
		case 3:
			unit.Source.Value.Attempt += 1 + int64(n)
		case 4:
			unit.Source.Value.NodeID += fmt.Sprint(n)
		case 5:
			unit.Source.Value.Team += fmt.Sprint(n)
		case 6:
			unit.Source.Value.RunID += fmt.Sprint(n)
		case 7:
			unit.Source.Set = false
		}
		if _, err := tenant.ReserveBlobUpload(t.Context(), forged); err == nil {
			return false
		}
		authority := request.Intent.Authority
		authority.Upload = runspec.Optional[blob.UploadIdentity]{Set: true, Value: forged.Intent.Identity}
		if _, err := tenant.LookupBlobUpload(t.Context(), authority, ticket.ID); err == nil {
			return false
		}
		if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, authority)); err == nil {
			return false
		}
		return tenant.AbortBlobUpload(t.Context(), authority, ticket.ID) != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128, Rand: rand.New(rand.NewSource(732))}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "uploads", 1)
	rewriteSchemaCount(t, s.db, "blob_receipts", 1)
}

func TestBlobEvalUploadRechecksIndependentPersistedFences(t *testing.T) {
	for _, problem := range []string{"unit_generation", "source_generation", "lease", "holder", "prefix", "status"} {
		t.Run(problem, func(t *testing.T) {
			s, tenant, request := evalUploadFixture(t)
			ticket, err := tenant.ReserveBlobUpload(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			switch problem {
			case "unit_generation":
				rewriteSchemaExec(t, s.db, "UPDATE eval_units SET generation=generation+1")
			case "source_generation":
				rewriteSchemaExec(t, s.db, "UPDATE nodes SET generation=generation+1")
			case "lease":
				rewriteSchemaExec(t, s.db, "UPDATE eval_units SET lease_expires_at=?", s.now().UnixNano())
			case "holder":
				rewriteSchemaExec(t, s.db, "UPDATE eval_units SET claimed_by='replacement'")
			case "prefix":
				rewriteSchemaExec(t, s.db, "UPDATE eval_units SET capability_prefix='replacement'")
			case "status":
				rewriteSchemaExec(t, s.db, "UPDATE eval_units SET status='done',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,claimed_at=NULL,finished_at=1")
			}
			request.ID = "replacement"
			if _, err := tenant.ReserveBlobUpload(t.Context(), request); err == nil {
				t.Fatal("stale eval reserved bytes")
			}
			if _, err := tenant.LookupBlobUpload(t.Context(), request.Intent.Authority, ticket.ID); err == nil {
				t.Fatal("stale eval looked up ticket")
			}
			if _, err := tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, request.Intent.Authority)); err == nil {
				t.Fatal("stale eval committed bytes")
			}
			if err := tenant.AbortBlobUpload(t.Context(), request.Intent.Authority, ticket.ID); err == nil {
				t.Fatal("stale eval released reservation")
			}
			rewriteSchemaCount(t, s.db, "blobs", 0)
			rewriteSchemaCount(t, s.db, "blob_receipts", 0)
			rewriteSchemaCount(t, s.db, "uploads", 1)
		})
	}
}
