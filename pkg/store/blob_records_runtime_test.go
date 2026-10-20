package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const fixtureMemoHash = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

type fakeBlobManifests map[blob.Digest][]blob.Member

func (f fakeBlobManifests) ReadBlobManifest(_ context.Context, _ Team, d blob.Digest) ([]blob.Member, error) {
	members, ok := f[d]
	if !ok {
		return nil, blob.ErrNotFound
	}
	return members, nil
}

func (f fakeBlobManifests) VerifyBlobManifest(ctx context.Context, team Team, digest blob.Digest, expected []blob.Member) error {
	members, err := f.ReadBlobManifest(ctx, team, digest)
	if err != nil {
		return err
	}
	if !slices.Equal(members, expected) {
		return blob.ErrIntegrity
	}
	return nil
}

func blobRecordFixture(t *testing.T) (*Store, *blobRecords, blob.UploadIntent) {
	t.Helper()
	s, tenant, actor, attempt := resultFixture(t)
	rewriteSchemaExec(t, s.db, "UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'", []byte(`{"id":"job","memo":{}}`))
	intent := blobIntent(actor, attempt)
	setBlobRecordRef(t, s, intent.Authority.CapabilityRef)
	intent.Authority.Ref.Scope = assertedRef("main")
	if _, err := commitTestBlob(t, tenant, intent); err != nil {
		t.Fatal(err)
	}
	return s, &blobRecords{tenant: tenant}, intent
}

func setBlobRecordRef(t *testing.T, s *Store, prefix string) {
	t.Helper()
	var body []byte
	if err := s.db.QueryRow("SELECT authority_json FROM capabilities WHERE prefix=?", prefix).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var record CapabilityRecord
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	scope := assertedRef("main").CapabilityScope()
	record.Value.Data.WriteRef = scope
	record.Value.Data.ReadRefs = []string{scope}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE capabilities SET authority_json=?,ref_scope=? WHERE prefix=?", body, scope, prefix)
	rewriteSchemaExec(t, s.db, "UPDATE runs SET request_json=? WHERE team='a' AND id='run'", []byte(`{"version":1,"request":{"git":{"ref":"main"}},"provenance":{"trust":"asserted","ref":"main","pipeline":"ci","writer":"account/fixture"}}`))
}

func blobOutputRequest(intent blob.UploadIntent) blob.CommitRecordRequest {
	return blob.CommitRecordRequest{Authority: intent.Authority, Source: intent.Identity, Key: blob.ResultRecordKey(intent.Identity.Attempt.Value, "gh:1", intent.Authority.Ref.Scope, "ci", blob.KindOutput), Digest: intent.Digest}
}

func addBlobJob(t *testing.T, s *Store, original blob.UploadIntent, id string) blob.UploadIntent {
	t.Helper()
	var c capability.Capability
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		p, err := tx.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: original.Authority.CapabilityRef})
		c = p.Capability
		return err
	}); err != nil {
		t.Fatal(err)
	}
	c.Prefix = id + "-claim"
	c.Digest = sha256.Sum256([]byte(c.Prefix))
	c.Binding.NodeID = id
	c.Data.NodeID = id
	var err error
	c.UnitID, err = encodeAwardUnit(UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: id})
	if err != nil {
		t.Fatal(err)
	}
	authorityPersist(t, s, c)
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,claimed_by,capability_prefix,generation,attempt,lease_expires_at,started_at,spec_json,execution_json) SELECT team,run_id,?,'running',claimed_by,?,generation,attempt,lease_expires_at,started_at,?,execution_json FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`, id, c.Prefix, []byte(`{"id":"`+id+`"}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) SELECT team,run_id,?,attempt,generation,holder,?,executor_kind,executor_name,executor_location,started_at FROM node_attempts WHERE team='a' AND run_id='run' AND node_id='job'`, id, c.Prefix)
	intent := original
	intent.Authority.CapabilityRef = c.Prefix
	intent.Authority.Attempt.Value.NodeID = id
	intent.Identity.CapabilityRef = c.Prefix
	intent.Identity.Attempt.Value.NodeID = id
	return intent
}

func TestBlobRecordsOwnOutputReplayAndScope(t *testing.T) {
	_, records, intent := blobRecordFixture(t)
	request := blobOutputRequest(intent)
	first, err := records.CommitRecord(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := records.CommitRecord(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.Provenance != blob.ProvenanceLocal || first.Source != intent.Identity {
		t.Fatalf("record drift: %+v %+v", first, second)
	}
	got, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: intent.Authority, Key: request.Key})
	if err != nil || !got.Found || got.Record != first {
		t.Fatalf("lookup=%+v,%v", got, err)
	}
	for _, problem := range []string{"team", "ref", "job", "run_key"} {
		bad := request
		switch problem {
		case "team":
			bad.Key.Team = "b"
		case "ref":
			bad.Key.Scope.Ref = blob.RefScope{Ref: "main", Verified: true}
		case "job":
			bad.Key.Scope.Job = "other"
		case "run_key":
			bad.Key.Scope.Key = "foreign-run/2/7"
		}
		if _, err := records.CommitRecord(t.Context(), bad); err == nil {
			t.Fatalf("accepted %s scope", problem)
		}
	}
}

func TestBlobRecordsRejectUnrelatedSameRunAndAllowDependency(t *testing.T) {
	s, records, first := blobRecordFixture(t)
	if _, err := records.CommitRecord(t.Context(), blobOutputRequest(first)); err != nil {
		t.Fatal(err)
	}
	second := addBlobJob(t, s, first, "consumer")
	request := blobOutputRequest(second)
	if _, err := records.CommitRecord(t.Context(), request); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatalf("laundered unrelated digest: %v", err)
	}
	presence, err := records.tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: second.Authority, Digests: []blob.Digest{first.Digest, blob.Digest(strings.Repeat("b", 64))}})
	if err != nil || len(presence.Present) != 0 {
		t.Fatalf("existence oracle: %+v,%v", presence, err)
	}
	finishBlobRecordJob(t, records, first)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='consumer'`, []byte(`{"id":"consumer","deps":["job"]}`))
	if _, err := records.CommitRecord(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	presence, err = records.tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: second.Authority, Digests: []blob.Digest{first.Digest, first.Digest}})
	if err != nil || len(presence.Present) != 1 {
		t.Fatalf("dependency unreadable: %+v,%v", presence, err)
	}
}

func TestBlobRecordsManifestChecksEveryMemberAndKeepsMetadata(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	member := blob.Member{Team: "a", ManifestDigest: blob.Digest(strings.Repeat("b", 64)), Digest: intent.Digest, Path: "bin/run", Mode: 0o755, Size: 3}
	manifests := fakeBlobManifests{member.ManifestDigest: []blob.Member{member}}
	records.manifests = manifests
	manifestIntent := intent
	manifestIntent.Digest = member.ManifestDigest
	if _, err := commitTestBlob(t, records.tenant, manifestIntent); err != nil {
		t.Fatal(err)
	}
	key := blob.ResultRecordKey(intent.Identity.Attempt.Value, "gh:1", intent.Authority.Ref.Scope, "ci", blob.KindManifest)
	request := blob.CommitRecordRequest{Authority: intent.Authority, Source: intent.Identity, Key: key, Digest: member.ManifestDigest, Members: []blob.Member{member}}
	if _, err := records.CommitRecord(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	got, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: intent.Authority, Key: key})
	if err != nil || len(got.Members) != 1 || got.Members[0] != member {
		t.Fatalf("manifest metadata=%+v,%v", got, err)
	}
	other := addBlobJob(t, s, intent, "other")
	other.Digest = blob.Digest(strings.Repeat("c", 64))
	if _, err := commitTestBlob(t, records.tenant, other); err != nil {
		t.Fatal(err)
	}
	forgedManifest := intent
	forgedManifest.Digest = blob.Digest(strings.Repeat("d", 64))
	if _, err := commitTestBlob(t, records.tenant, forgedManifest); err != nil {
		t.Fatal(err)
	}
	forged := member
	forged.ManifestDigest, forged.Digest = forgedManifest.Digest, other.Digest
	manifests[forgedManifest.Digest] = []blob.Member{forged}
	request.Digest, request.Members = forgedManifest.Digest, []blob.Member{forged}
	if _, err := records.CommitRecord(t.Context(), request); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatalf("foreign member accepted: %v", err)
	}
	rewriteSchemaCount(t, s.db, "blob_records", 1)
	rewriteSchemaCount(t, s.db, "blob_members", 1)
}

func TestBlobRecordsMemoCopyAndDepcachePointers(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	memo := blobOutputRequest(intent)
	memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if _, err := records.CommitRecord(t.Context(), memo); err != nil {
		t.Fatal(err)
	}
	lookup := blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: assertedRef("main"), Pipeline: "ci", Job: "job", Hash: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}}
	hit, err := records.LookupMemo(t.Context(), lookup)
	if err != nil || !hit.Found || hit.Output.Value != intent.Digest {
		t.Fatalf("memo=%+v,%v", hit, err)
	}
	for range 2 {
		copied, err := records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: lookup, Destination: intent.Identity.Attempt.Value})
		if err != nil || !copied.Found {
			t.Fatalf("copy=%+v,%v", copied, err)
		}
	}
	rewriteSchemaCount(t, s.db, "blob_records", 2)
	dep := blobOutputRequest(intent)
	dep.Key = blob.RecordKey{Team: "a", Kind: blob.KindDepcache, Scope: blob.Scope{Repo: "gh:1", Ref: assertedRef("main"), Key: "v1"}, Name: "modules"}
	if _, err := records.CommitRecord(t.Context(), dep); err != nil {
		t.Fatal(err)
	}
	cache, err := records.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: intent.Authority, Key: blob.DepcacheKey{Repo: "gh:1", Ref: assertedRef("main"), Name: "modules", Key: "v1"}})
	if err != nil || !cache.Found || cache.Source != blob.RestoreOwnRef || cache.Record.Digest != intent.Digest {
		t.Fatalf("depcache=%+v,%v", cache, err)
	}
	lookup.Key.Ref = blob.RefScope{Ref: "main", Verified: true}
	if _, err := records.LookupMemo(t.Context(), lookup); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatalf("manual caller selected verified scope: %v", err)
	}
}

func TestBlobRecordsMemoCopyRollsBackBothDestinations(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	manifest := intent
	manifest.Digest = blob.Digest(strings.Repeat("b", 64))
	if _, err := commitTestBlob(t, records.tenant, manifest); err != nil {
		t.Fatal(err)
	}
	members := []blob.Member{{Team: "a", ManifestDigest: manifest.Digest, Digest: intent.Digest, Path: "output", Mode: 0o644, Size: 3}}
	records.manifests = fakeBlobManifests{manifest.Digest: members}
	memo := blobOutputRequest(intent)
	memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	memo.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: manifest.Digest}
	memo.Members = members
	if _, err := records.CommitRecord(t.Context(), memo); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_manifest_copy BEFORE INSERT ON blob_records WHEN NEW.kind='manifest' BEGIN SELECT RAISE(ABORT,'manifest insert unavailable'); END`)
	request := blob.CopyMemoRequest{Lookup: blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: assertedRef("main"), Pipeline: "ci", Job: "job", Hash: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}}, Destination: intent.Identity.Attempt.Value}
	if _, err := records.CopyMemo(t.Context(), request); err == nil {
		t.Fatal("partial copy accepted")
	}
	rewriteSchemaCount(t, s.db, "blob_records", 1)
	rewriteSchemaExec(t, s.db, "DROP TRIGGER refuse_manifest_copy")
	hit, err := records.CopyMemo(t.Context(), request)
	if err != nil || !hit.Found {
		t.Fatalf("copy=%+v,%v", hit, err)
	}
	rewriteSchemaCount(t, s.db, "blob_records", 3)
}

func TestBlobRecordsCacheSurvivesSourceRetention(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	memo := blobOutputRequest(intent)
	memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	original, err := records.CommitRecord(t.Context(), memo)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
	rewriteSchemaCount(t, s.db, "blob_receipts", 0)
	rewriteSchemaCount(t, s.db, "blob_records", 1)
	scope, err := blob.RecordNamespace(memo.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		record, found, err := readBlobRecordTx(t.Context(), tx, memo.Key, scope)
		if !found || record.Source != original.Source || record.RefFacts != original.RefFacts {
			t.Fatal("retention discarded source provenance")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "gc", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if marked, err := s.AsOperator().MarkBlobTombstones(t.Context(), lease, 10); err != nil || marked != 0 {
		t.Fatalf("retained memo was collected: %d,%v", marked, err)
	}
}

type blobManifestFunc func(context.Context, Team, blob.Digest) ([]blob.Member, error)

func (f blobManifestFunc) ReadBlobManifest(ctx context.Context, team Team, digest blob.Digest) ([]blob.Member, error) {
	return f(ctx, team, digest)
}

func (f blobManifestFunc) VerifyBlobManifest(ctx context.Context, team Team, digest blob.Digest, expected []blob.Member) error {
	members, err := f(ctx, team, digest)
	if err != nil {
		return err
	}
	if !slices.Equal(members, expected) {
		return blob.ErrIntegrity
	}
	return nil
}

func TestBlobRecordsAuthorizeManifestBeforeReadingBytes(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	other := addBlobJob(t, s, intent, "other")
	other.Digest = blob.Digest(strings.Repeat("c", 64))
	if _, err := commitTestBlob(t, records.tenant, other); err != nil {
		t.Fatal(err)
	}
	called := false
	records.manifests = blobManifestFunc(func(context.Context, Team, blob.Digest) ([]blob.Member, error) { called = true; return nil, nil })
	request := blobOutputRequest(intent)
	request.Key.Kind = blob.KindManifest
	request.Digest = other.Digest
	if _, err := records.CommitRecord(t.Context(), request); !errors.Is(err, blob.ErrDigestNotReadable) || called {
		t.Fatalf("foreign manifest read: called=%v,err=%v", called, err)
	}
}

func finishBlobRecordJob(t *testing.T, records *blobRecords, intent blob.UploadIntent) {
	t.Helper()
	records.tenant.s.SetResultBlobRecords(records.manifests)
	id := intent.Identity.Attempt.Value
	key := blobOutputRequest(intent).Key
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: intent.Authority.CapabilityRef}, CapabilityPrefix: intent.Authority.CapabilityRef, Holder: "machine", Generation: id.Generation}
	_, err := records.tenant.TransitionNode(t.Context(), NodeTransition{Ref: NodeRef{id.RunID, id.NodeID}, Actor: actor, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(id.Attempt), Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeSuccess, OutputRecord: &key}}})
	if err != nil {
		t.Fatal(err)
	}
}
