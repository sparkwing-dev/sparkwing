package store

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestDataProjectionAndChargeUseLiveAuthorityProperty(t *testing.T) {
	s, tenant, actor, attempt := resultFixture(t)
	intent := blobIntent(actor, attempt)
	if _, err := commitTestBlob(t, tenant, intent); err != nil {
		t.Fatal(err)
	}
	a := dataAccessAuthority(t, s, actor.CapabilityPrefix)
	a.Target.Team = "a"
	resolved, err := tenant.ResolveDataAuthority(t.Context(), a)
	if err != nil || resolved.CapabilityRef != intent.Authority.CapabilityRef || resolved.Principal != intent.Authority.Principal || resolved.Attempt != intent.Authority.Attempt || !resolved.Upload.Set || resolved.Upload.Value != intent.Identity || resolved.Repo != "gh:1" {
		t.Fatalf("native data projection: %+v, %v", resolved, err)
	}
	metadata, err := tenant.ReadDataBlob(t.Context(), resolved, intent.Digest)
	if err != nil || metadata.Size != intent.Size {
		t.Fatalf("native data metadata: %+v, %v", metadata, err)
	}
	random := rand.New(rand.NewPCG(871, 310))
	for i := range 64 {
		input := resolved
		switch random.IntN(4) {
		case 0:
			input.Principal = blob.Principal(fmt.Sprintf("foreign-%x", random.Uint64()))
		case 1:
			input.Repo = "foreign-repository"
		case 2:
			input.Attempt.Value.Generation++
		case 3:
			input.CapabilityRef += "-foreign"
		}
		if _, err := tenant.ReadDataBlob(t.Context(), input, intent.Digest); err == nil {
			t.Fatalf("forged data identity %d read bytes", i)
		}
		if _, err := tenant.ChargeDataDownload(t.Context(), input, metadata, DownloadCharge{ReplicaID: "controller", Bytes: metadata.Size}); err == nil {
			t.Fatalf("forged data identity %d committed a download", i)
		}
	}
	if _, err := tenant.ChargeDataDownload(t.Context(), resolved, metadata, DownloadCharge{ReplicaID: "controller", Bytes: metadata.Size}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `DELETE FROM blob_receipts WHERE team='a' AND digest=?`, intent.Digest)
	if _, err := tenant.ChargeDataDownload(t.Context(), resolved, metadata, DownloadCharge{ReplicaID: "controller", Bytes: metadata.Size}); err == nil {
		t.Fatal("removed read edge still committed a download")
	}
	var bytes int64
	if err := s.db.QueryRow(`SELECT SUM(bytes) FROM team_download_day WHERE team='a'`).Scan(&bytes); err != nil || bytes != metadata.Size {
		t.Fatalf("refused downloads changed counters: %d, %v", bytes, err)
	}
	a.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "missing"}
	if _, err := tenant.ResolveDataAuthority(t.Context(), a); err == nil {
		t.Fatal("missing authority projected an uploader")
	}
}

func TestDataLookupAuthorizesSelectionBeforeSelectingDigest(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	request := blobOutputRequest(intent)
	if _, err := records.CommitRecord(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	a := dataAccessAuthority(t, s, intent.Authority.CapabilityRef)
	target := capability.DataTarget{Operation: capability.DataLookup, RecordKind: string(request.Key.Kind), RecordScope: request.Key.Scope.Ref.Ref, RecordName: request.Key.Name}
	if err := records.tenant.CheckDataAccess(t.Context(), a, target); err != nil {
		t.Fatalf("logical record cannot select a digest: %v", err)
	}
	if result, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: intent.Authority, Key: request.Key}); err != nil || !result.Found {
		t.Fatalf("authorized record selection: %+v, %v", result, err)
	}
}

func TestDataCancellationClosesSensitiveAccessWithoutEndingHolder(t *testing.T) {
	for _, table := range []string{"runs", "nodes"} {
		t.Run(table, func(t *testing.T) {
			s, tenant, actor, attempt := resultFixture(t)
			intent := blobIntent(actor, attempt)
			if _, err := commitTestBlob(t, tenant, intent); err != nil {
				t.Fatal(err)
			}
			a := dataAccessAuthority(t, s, actor.CapabilityPrefix)
			a.Target.Team = "a"
			authority, err := tenant.ResolveDataAuthority(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := tenant.ReadDataBlob(t.Context(), authority, intent.Digest)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, s.db, `UPDATE `+table+` SET cancel_requested_at=1 WHERE team='a'`)
			if _, err := tenant.ResolveDataAuthority(t.Context(), a); err == nil {
				t.Fatal("cancelled work retained sensitive data authority")
			}
			if _, err := tenant.ReadDataBlob(t.Context(), authority, intent.Digest); err == nil {
				t.Fatal("cancelled work retained blob reads")
			}
			if _, err := tenant.ChargeDataDownload(t.Context(), authority, metadata, DownloadCharge{ReplicaID: "controller", Bytes: metadata.Size}); err == nil {
				t.Fatal("cancelled work committed signed-byte usage")
			}
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				row, err := readNodeTx(t.Context(), tx, tenant.team, controllerwire.NodeRef{RunID: "run", NodeID: "job"})
				if err != nil {
					return err
				}
				return checkHolderTx(t.Context(), tx, tenant.team, row, actor, tx.now())
			}); err != nil {
				t.Fatalf("data cancellation ended holder rights: %v", err)
			}
		})
	}
}
