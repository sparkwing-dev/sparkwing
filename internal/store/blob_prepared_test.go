package store

import (
	"encoding/json"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"strings"
	"testing"
	"time"
)

func TestPreparedPipelineOwnerPublishAndAwardRead(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan} {
		t.Run(string(kind), func(t *testing.T) {
			s, claim := releaseFixture(t, kind)
			tenant := &Tenant{s: s, team: "a"}
			identity := blob.UploadIdentity{Kind: blob.UploadOwnerUnit, Team: "a", RunID: "run", Principal: "machine", CapabilityRef: claim.Award.CapabilityPrefix, Unit: runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadUnitKind(kind), ID: "run", Generation: 7}}}
			authority := blob.Authority{CapabilityRef: identity.CapabilityRef}
			setBlobRecordRef(t, s, identity.CapabilityRef)
			rewriteSchemaExec(t, s.db, "UPDATE runs SET request_json=? WHERE team='a' AND id='run'", []byte(`{"version":1,"request":{"git":{"repository":{"key":"gh:1"},"ref":"main"}},"provenance":{"trust":"asserted","ref":"main","pipeline":"ci","writer":"account/fixture"}}`))
			ticket, err := tenant.ReserveBlobUpload(t.Context(), UploadReservationRequest{ID: "prepared", Intent: blob.UploadIntent{Authority: authority, Identity: identity, Digest: blob.Digest(strings.Repeat("a", 64)), Size: 3}, ExpiresAt: s.now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, authority)); err != nil {
				t.Fatal(err)
			}
			key := blob.PreparedRecordKey("a", "run", "gh:1", "ci")
			request := blob.CommitRecordRequest{Authority: authority, Source: identity, Key: key, Digest: ticket.Blob.Digest}
			records := tenant.BlobRecords(nil)
			if _, err = records.CommitRecord(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if _, err = records.CommitRecord(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			second, err := tenant.ReserveBlobUpload(t.Context(), UploadReservationRequest{ID: "changed", Intent: blob.UploadIntent{Authority: authority, Identity: identity, Digest: blob.Digest(strings.Repeat("b", 64)), Size: 3}, ExpiresAt: s.now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tenant.CommitBlobUpload(t.Context(), verifiedUpload(second, authority)); err != nil {
				t.Fatal(err)
			}
			changed := request
			changed.Digest = second.Blob.Digest
			if _, err = records.CommitRecord(t.Context(), changed); err == nil {
				t.Fatal("changed prepared bytes accepted")
			}
			var run string
			if err = s.db.QueryRow("SELECT run_id FROM blob_records WHERE kind='prepared'").Scan(&run); err != nil || run != "run" {
				t.Fatalf("retention run=%q: %v", run, err)
			}
			for _, field := range []string{"run", "team", "job", "name"} {
				forged := request
				switch field {
				case "run":
					forged.Key.Scope.Key = "other"
				case "team":
					forged.Key.Team = "b"
				case "job":
					forged.Key.Scope.Job = "job"
				case "name":
					forged.Key.Name = "other"
				}
				if _, err = records.CommitRecord(t.Context(), forged); err == nil {
					t.Fatalf("accepted forged %s", field)
				}
			}
			unit, err := encodeAwardUnit(UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"})
			if err != nil {
				t.Fatal(err)
			}
			var issued capability.Issued
			if err = s.inTx(t.Context(), func(tx *storeTx) error {
				var e error
				issued, e = issueFixtureAward(t, s, tx, capability.AwardSpec{Claimant: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "machine"}, Unit: capability.UnitNode, UnitID: unit, Purpose: capability.PurposeWork, Delivery: capability.DeliveryDirect})
				return e
			}); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE nodes SET capability_prefix=?,status='pending',started_at=NULL WHERE team='a' AND run_id='run' AND node_id='job'", issued.Capability.Prefix)
			reader := blob.Authority{CapabilityRef: issued.Capability.Prefix}

			hit, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: reader, Key: key})
			if err != nil || !hit.Found {
				t.Fatalf("award read: %+v: %v", hit, err)
			}
			presence, err := tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: reader, Digests: []blob.Digest{ticket.Blob.Digest}})
			if err != nil || len(presence.Present) != 1 {
				t.Fatalf("award bytes: %+v: %v", presence, err)
			}
			otherTeam := key
			otherTeam.Team = "b"
			if _, err = records.LookupRecord(t.Context(), blob.RecordLookup{Authority: reader, Key: otherTeam}); err == nil {
				t.Fatal("cross-team prepared read accepted")
			}
			otherRun := identity
			otherRun.RunID = "other"
			otherSource, err := json.Marshal(otherRun)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE blob_records SET source_identity_json=? WHERE kind='prepared'", otherSource)
			if _, err = records.LookupRecord(t.Context(), blob.RecordLookup{Authority: reader, Key: key}); err == nil {
				t.Fatal("cross-run prepared record was readable")
			}
			originalSource, err := json.Marshal(identity)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, "UPDATE blob_records SET source_identity_json=?,ref_facts_json=? WHERE kind='prepared'", originalSource, []byte(`{"Scope":{"Ref":"main","Writer":"other"}}`))
			if _, err = records.LookupRecord(t.Context(), blob.RecordLookup{Authority: reader, Key: key}); err == nil {
				t.Fatal("tampered provenance was readable")
			}
			request.Authority = reader
			if _, err = records.CommitRecord(t.Context(), request); err == nil {
				t.Fatal("job wrote prepared record")
			}
		})
	}
}

func TestPreparedPipelineEvalCannotPublish(t *testing.T) {
	s, tenant, upload := evalUploadFixture(t)
	setBlobRecordRef(t, s, upload.Intent.Authority.CapabilityRef)
	ticket, err := tenant.ReserveBlobUpload(t.Context(), upload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tenant.CommitBlobUpload(t.Context(), verifiedUpload(ticket, upload.Intent.Authority)); err != nil {
		t.Fatal(err)
	}
	request := blob.CommitRecordRequest{Authority: upload.Intent.Authority, Source: upload.Intent.Identity, Key: blob.PreparedRecordKey("a", "run", "gh:1", "ci"), Digest: ticket.Blob.Digest}
	if _, err = tenant.BlobRecords(nil).CommitRecord(t.Context(), request); err == nil {
		t.Fatal("eval published prepared pipeline")
	}
}
