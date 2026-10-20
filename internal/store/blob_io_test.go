package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type probingManifests struct {
	fakeBlobManifests
	probe func(kind string, digest blob.Digest)
}

func (p probingManifests) ReadBlobManifest(ctx context.Context, team Team, digest blob.Digest) ([]blob.Member, error) {
	p.probe("read", digest)
	return p.fakeBlobManifests.ReadBlobManifest(ctx, team, digest)
}

func (p probingManifests) VerifyBlobManifest(ctx context.Context, team Team, digest blob.Digest, members []blob.Member) error {
	p.probe("verify", digest)
	return p.fakeBlobManifests.VerifyBlobManifest(ctx, team, digest, members)
}

func (p probingManifests) VerifyBlob(_ context.Context, object blob.Blob) error {
	p.probe("blob", object.Digest)
	return nil
}

// safety: the fixture database has one connection, so this write completes only outside a transaction.
func probeWithoutTransaction(t *testing.T, s *Store, calls map[string]int) func(string, blob.Digest) {
	return func(kind string, _ blob.Digest) {
		calls[kind]++
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if _, err := s.db.ExecContext(ctx, "UPDATE runs SET pipeline=pipeline WHERE team='a'"); err != nil {
			t.Errorf("%s ran inside a transaction: %v", kind, err)
		}
	}
}

func memoManifestRequest(intent blob.UploadIntent, manifest blob.Digest, members []blob.Member) blob.CommitRecordRequest {
	memo := blobOutputRequest(intent)
	memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", fixtureMemoHash
	memo.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: manifest}
	memo.Members = members
	return memo
}

func TestBlobObjectReadsRunOutsideTransactions(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET spec_hash='same'")
	output := blobOutputRequest(intent)
	if _, err := records.CommitRecord(t.Context(), output); err != nil {
		t.Fatal(err)
	}
	manifest := intent
	manifest.Digest = blob.Digest(strings.Repeat("b", 64))
	if _, err := commitTestBlob(t, records.tenant, manifest); err != nil {
		t.Fatal(err)
	}
	members := []blob.Member{{Team: "a", ManifestDigest: manifest.Digest, Digest: intent.Digest, Path: "result", Size: intent.Size, Mode: 0o644}}
	calls := map[string]int{}
	probing := probingManifests{fakeBlobManifests{manifest.Digest: members}, probeWithoutTransaction(t, s, calls)}
	records.manifests = probing
	{
		records := &blobRecords{manifests: probing}
		s.resultRecords, s.materializations = records, records
	}
	artifacts := blobOutputRequest(manifest)
	artifacts.Key.Kind, artifacts.Members = blob.KindManifest, members
	if _, err := records.CommitRecord(t.Context(), artifacts); err != nil {
		t.Fatal(err)
	}
	if _, err := records.CommitRecord(t.Context(), memoManifestRequest(intent, manifest.Digest, members)); err != nil {
		t.Fatal(err)
	}
	if got, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: intent.Authority, Key: artifacts.Key}); err != nil || len(got.Members) != 1 {
		t.Fatalf("lookup=%+v,%v", got, err)
	}
	if hit, err := records.LookupMemo(t.Context(), fixtureMemoLookup(intent)); err != nil || !hit.Found || len(hit.Members) != 1 {
		t.Fatalf("memo=%+v,%v", hit, err)
	}
	a := intent.Identity.Attempt.Value
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: intent.Authority.CapabilityRef}, CapabilityPrefix: intent.Authority.CapabilityRef, Holder: "machine", Generation: a.Generation}
	ref := controllerwire.NodeRef{RunID: a.RunID, NodeID: a.NodeID}
	report := NodeTransition{Ref: ref, Actor: actor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(a.Attempt), Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeSuccess, OutputRecord: &output.Key, ManifestRecord: &artifacts.Key}}}
	if _, err := records.tenant.TransitionNode(t.Context(), report); err != nil {
		t.Fatal(err)
	}
	_, tenant, request := materializationTarget(t, s, records.tenant, ref)
	if got, err := tenant.ReconcileNode(t.Context(), request); err != nil || got.Kind != Materialize {
		t.Fatalf("materialization: %+v: %v", got, err)
	}
	if calls["verify"] < 2 || calls["read"] < 4 || calls["blob"] < 1 {
		t.Fatalf("object reads not exercised: %v", calls)
	}
}

func fixtureMemoLookup(intent blob.UploadIntent) blob.MemoLookup {
	return blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: assertedRef("main"), Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}}
}

// A memo repointed while its manifest is read is reread, never answered with stale members,
// and one that keeps moving fails rather than answering.
func TestBlobLookupRereadsARepointedManifest(t *testing.T) {
	for _, flapping := range []bool{false, true} {
		s, records, intent := blobRecordFixture(t)
		var digests []blob.Digest
		fake := fakeBlobManifests{}
		for _, c := range "bcdef" {
			object := intent
			object.Digest = blob.Digest(strings.Repeat(string(c), 64))
			if _, err := commitTestBlob(t, records.tenant, object); err != nil {
				t.Fatal(err)
			}
			digests = append(digests, object.Digest)
			fake[object.Digest] = []blob.Member{{Team: "a", ManifestDigest: object.Digest, Digest: intent.Digest, Path: string(c), Size: intent.Size, Mode: 0o644}}
		}
		records.manifests = fake
		for _, digest := range slices.Backward(digests) {
			if _, err := records.CommitRecord(t.Context(), memoManifestRequest(intent, digest, fake[digest])); err != nil {
				t.Fatal(err)
			}
		}
		records.manifests = probingManifests{fake, func(_ string, read blob.Digest) {
			if i := slices.Index(digests, read); i+1 < len(digests) && (flapping || i == 0) {
				rewriteSchemaExec(t, s.db, "UPDATE blob_records SET manifest_digest=? WHERE kind='memo'", digests[i+1])
			}
		}}
		hit, err := records.LookupMemo(t.Context(), fixtureMemoLookup(intent))
		if flapping {
			if err == nil || hit.Found {
				t.Fatalf("moving memo answered: %+v", hit)
			}
			continue
		}
		if err != nil || !hit.Found || hit.Manifest.Value != digests[1] || len(hit.Members) != 1 || hit.Members[0].Path != "c" {
			t.Fatalf("repointed memo=%+v,%v", hit, err)
		}
	}
}

// A source record removed while its bytes are verified turns materialization into a miss.
func TestBlobMaterializationRecheckAfterVerification(t *testing.T) {
	s, tenant, request := blobMaterializationFixture(t)
	request.MaterializeOnly = true
	fake := tenant.s.materializations.(*blobRecords).manifests.(fakeBlobManifests)
	{
		records := &blobRecords{manifests: probingManifests{fake, func(kind string, _ blob.Digest) {
			if kind == "blob" {
				rewriteSchemaExec(t, s.db, "DELETE FROM blob_records WHERE kind='output'")
			}
		}}}
		s.resultRecords, s.materializations = records, records
	}
	got, err := tenant.ReconcileNode(t.Context(), request)
	if err != nil || got.Kind != Dispatch || got.Node.OutputRecord != nil {
		t.Fatalf("stale verification materialized: %+v: %v", got, err)
	}
	var copied int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM blob_records WHERE team='a' AND run_id='target'").Scan(&copied); err != nil || copied != 0 {
		t.Fatalf("stale verification copied %d records: %v", copied, err)
	}
}
