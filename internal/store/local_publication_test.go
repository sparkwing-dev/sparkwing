package store

import (
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestLocalPublicationReadsTenDistinctManifests(t *testing.T) {
	s, records, first := blobRecordFixture(t)
	intents := []blob.UploadIntent{first}
	for i := 1; i < 10; i++ {
		intents = append(intents, addBlobJob(t, s, first, fmt.Sprintf("job-%d", i)))
	}
	manifests := fakeBlobManifests{}
	records.manifests = manifests
	{
		records := &blobRecords{manifests: manifests}
		s.resultRecords, s.materializations = records, records
	}
	for i, intent := range intents {
		intent.Digest = blob.Digest(fmt.Sprintf("%064x", i+1))
		if _, err := commitTestBlob(t, records.tenant, intent); err != nil {
			t.Fatal(err)
		}
		manifests[intent.Digest] = nil
		request := blobOutputRequest(intent)
		request.Key.Kind = blob.KindManifest
		if _, err := records.CommitRecord(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		a := intent.Identity.Attempt.Value
		actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: intent.Authority.CapabilityRef}, CapabilityPrefix: intent.Authority.CapabilityRef, NodeFence: runspec.NodeFence{RunID: runspec.RunID(a.RunID), NodeID: runspec.NodeID(a.NodeID), Holder: "machine", Generation: runspec.ClaimGeneration(a.Generation)}}
		change := nodeTransition{Ref: controllerwire.NodeRef{RunID: runspec.RunID(a.RunID), NodeID: runspec.NodeID(a.NodeID)}, Actor: actor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(a.Attempt), Extra: transitionExtra{Result: &NodeResult{Outcome: NodeSuccess, ManifestRecord: &request.Key}}}
		if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
			t.Fatal(err)
		}
	}
	rewriteSchemaExec(t, s.db, "UPDATE runs SET status='success',finished_at=? WHERE team='a' AND id='run'", s.now().UnixNano())
	calls := map[blob.Digest]int{}
	{
		records := &blobRecords{manifests: probingManifests{manifests, func(kind string, digest blob.Digest) {
			if kind == "read" {
				calls[digest]++
			}
		}}}
		s.resultRecords, s.materializations = records, records
	}
	publication, err := records.tenant.ReadLocalPublication(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if len(publication.Records) != 10 || len(publication.Results) != 10 || len(calls) != 10 {
		t.Fatalf("publication has %d records, %d results, %d manifest reads", len(publication.Records), len(publication.Results), len(calls))
	}
	for digest, count := range calls {
		if count != 1 {
			t.Errorf("manifest %s read %d times", digest, count)
		}
	}
}
