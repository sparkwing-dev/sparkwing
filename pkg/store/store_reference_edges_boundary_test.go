package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func checkStoreReferenceBoundary(t *testing.T) error {
	ctx := t.Context()
	s, tenant, authority, digests := seedStoreReferenceBoundary(t)
	blobAccountingDiskStore(t, s)
	reader := authority.Attempt.Value
	authorized := capability.Authorized{Principal: capability.Principal{Capability: capability.Capability{Prefix: authority.CapabilityRef, Team: reader.Team}}}
	for _, test := range []struct {
		name    string
		spec    nodeproto.JobSpec
		allowed bool
	}{
		{"unrelated", nodeproto.JobSpec{}, false},
		{"dependency", nodeproto.JobSpec{Needs: []string{"job"}}, true},
		{"optional_dependency", nodeproto.JobSpec{NeedsIfPresent: []string{"job"}}, true},
		{"transitive_dependency", nodeproto.JobSpec{Needs: []string{"unrelated"}}, true},
		{"artifact_input", nodeproto.JobSpec{Consumes: []nodeproto.ArtifactInput{{Producer: "job"}}}, true},
		{"generated_group", nodeproto.JobSpec{NeedsGenerated: []string{"generated"}}, true},
		{"failure_handler", nodeproto.JobSpec{OnFailureOf: "job"}, true},
	} {
		test.spec.ID = reader.NodeID
		body, err := json.Marshal(test.spec)
		if err != nil {
			return err
		}
		if _, err := s.DB().ExecContext(ctx, "UPDATE nodes SET spec_json=? WHERE team=? AND run_id='run' AND node_id='b'", body, tenant.Team()); err != nil {
			return err
		}
		presence, err := tenant.ReadableBlobs(ctx, blob.HasManyRequest{Authority: authority, Digests: digests})
		var want []blob.Digest
		if test.allowed {
			want = digests[:3]
		}
		if err != nil || !slices.Equal(presence.Present, want) {
			return fmt.Errorf("%s read set=%+v, want %v: %v", test.name, presence, want, err)
		}
		for i, digest := range digests {
			for _, operation := range []capability.DataOperation{capability.DataDownload, capability.DataRecord} {
				target := capability.DataTarget{Operation: operation, Digest: string(digest), RecordKind: string(blob.KindOutput), RunID: reader.RunID, NodeID: reader.NodeID, Attempt: reader.Attempt, Generation: reader.Generation}
				err := tenant.CheckDataAccess(ctx, authorized, target)
				allowed := test.allowed && i < 3
				if allowed && err != nil || !allowed && err == nil {
					return fmt.Errorf("%s %s digest %d allowed=%v: %v", test.name, operation, i, allowed, err)
				}
			}
		}
	}
	for _, table := range []string{"team_download_day", "egress_usage"} {
		var count int
		if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("reference checks consumed %s bytes", table)
		}
	}
	return nil
}

func seedStoreReferenceBoundary(t *testing.T) (*Store, *Tenant, blob.Authority, []blob.Digest) {
	t.Helper()
	s, records, intent := blobRecordFixture(t)
	reader := addBlobJob(t, s, intent, "b")
	unrelated := addBlobJob(t, s, intent, "unrelated")
	var digests []blob.Digest
	for _, letter := range []string{"a", "b", "c", "d"} {
		object := intent
		if letter == "d" {
			object = unrelated
		}
		object.Digest = blob.Digest(strings.Repeat(letter, 64))
		if _, err := commitTestBlob(t, records.tenant, object); err != nil {
			t.Fatal(err)
		}
		digests = append(digests, object.Digest)
	}
	members := []blob.Member{{Team: "a", ManifestDigest: digests[1], Digest: digests[2], Path: "artifact", Size: 3, Mode: 0o644}}
	records.manifests = fakeBlobManifests{digests[1]: members}
	output := blobOutputRequest(intent)
	output.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: digests[1]}
	output.Members = members
	if _, err := records.CommitRecord(t.Context(), output); err != nil {
		t.Fatal(err)
	}
	source := intent.Identity.Attempt.Value
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: intent.Authority.CapabilityRef}, CapabilityPrefix: intent.Authority.CapabilityRef, Holder: "machine", Generation: source.Generation}
	s.SetResultBlobRecords(records.manifests)
	if _, err := records.tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{source.RunID, source.NodeID}, Actor: actor, Cause: CauseReport, To: NodeSuccess, SourceAttempt: sourceAttempt(source.Attempt), Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeSuccess, OutputRecord: &output.Key}}}); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'", []byte(`{"id":"job","groups":["generated"]}`))
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='unrelated'", []byte(`{"id":"unrelated","deps":["job"]}`))
	foreign, object := storeBoundaryForeignUploader(t, s)
	if _, err := commitTestBlob(t, foreign, object); err != nil {
		t.Fatal(err)
	}
	digests = append(digests, object.Digest, blob.Digest(strings.Repeat("f", 64)))
	return s, records.tenant, reader.Authority, digests
}

func TestStoreReferenceEdges(t *testing.T) {
	if err := checkStoreReferenceBoundary(t); err != nil {
		t.Fatal(err)
	}
}

func storeBoundaryForeignUploader(t *testing.T, s *Store) (*Tenant, blob.UploadIntent) {
	t.Helper()
	maintenanceRun(t, s, "foreign", 2, RunRunning)
	rewriteSchemaExec(t, s.db, "UPDATE runs SET team='b',intake_mode='publish',created_principal='publisher' WHERE id='foreign'")
	publisher := authorityRoot("foreign-publisher", "b")
	publisher.Subject.ID = "publisher"
	publisher.Grants = []capability.Grant{capability.DataRead, capability.DataWrite}
	publisher.Binding = capability.Binding{Kind: capability.BindingRun, RunID: "foreign"}
	publisher.Data = &capability.DataScope{Use: capability.DataPublish, RepositoryKey: "gh:1", RunID: "foreign", WriteRef: "manual:", ReadRefs: []string{"manual:"}}
	authorityPersist(t, s, publisher)
	foreign := &Tenant{s: s, team: "b"}
	object := blob.UploadIntent{Authority: blob.Authority{CapabilityRef: publisher.Prefix}, Identity: blob.UploadIdentity{Kind: blob.UploadPublication, Team: "b", RunID: "foreign", Principal: "publisher", CapabilityRef: publisher.Prefix}, Digest: blob.Digest(strings.Repeat("e", 64)), Size: 3}
	return foreign, object
}
