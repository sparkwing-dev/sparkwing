package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	depcacheblob "github.com/sparkwing-dev/sparkwing/internal/blob/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/depcachekey"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestStorageSeededAuthorityUsesStoreClock(t *testing.T) {
	now := time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)
	s, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"), teststore.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	authority, _, err := seedDepcacheBoundary(t, t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: authority.CapabilityRef})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Capability.IssuedAt.Equal(now) {
		t.Fatalf("storage authority issued at %s, want %s", p.Capability.IssuedAt, now)
	}
}

func checkBlobDepcache(t testing.TB, ctx context.Context, root string, objects blob.ObjectBackend, transfers blob.HTTPClient, corrupt func(string) error, seed [16]byte) (err error) {
	path := filepath.Join(root, "archives.db")
	now := time.Now().UTC()
	s, dataController, err := openStorageData(path, objects, now)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	_, identity, err := seedDepcacheBoundary(t, ctx, s)
	if err != nil {
		return err
	}
	binding, err := bindStorageData(ctx, s, dataController, identity.CapabilityRef)
	if err != nil {
		return err
	}
	authority, identity := binding.Authority, binding.Authority.Upload.Value
	settlement, receipt, err := settleStorageProbe(ctx, binding, objects, transfers, seed[:])
	if err != nil {
		return err
	}
	data := depcacheblob.Data{Bytes: blob.NewSignedStore(blob.SignedStoreOptions{Signer: binding.Signer, HTTPClient: transfers}), Records: binding.Records, Authority: authority, Identity: identity, Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}}
	output := blob.OutputData{Bytes: data.Bytes, Records: data.Records, Authority: authority, Identity: identity, Pipeline: "fixture", Budget: data.Budget}
	attempt := identity.Attempt.Value
	accepted := runspec.Spec{Hash: "spec", PlanHash: "plan", Request: runspec.SealedRequest{Request: runspec.Request{Team: identity.Team, Pipeline: "fixture", Git: runspec.Git{Repository: runspec.Repository{Key: authority.Repo}}}}}
	raw, err := json.Marshal(accepted)
	if err != nil {
		return err
	}
	input := nodehost.NodeInput{Accepted: nodehost.AcceptedRun{RunID: identity.RunID, Spec: raw, SpecHash: string(accepted.Hash), PlanHash: string(accepted.PlanHash)}, NodeID: attempt.NodeID, Attempt: runspec.AttemptOrdinal(attempt.Attempt), Generation: runspec.ClaimGeneration(attempt.Generation), Authority: nodehost.ClaimAuthority{Holder: "logger", CapabilityPrefix: identity.CapabilityRef}, Storage: nodehost.StorageBinding{Bytes: data.Bytes, Records: data.Records, Authority: authority}}
	body := seed[:]
	if seed[0]%2 == 0 {
		body = nil
	}
	var result blob.Record
	if body == nil {
		result, err = output.Put(ctx, body)
	} else {
		body, err = json.Marshal(seed)
		if err != nil {
			return err
		}
		writer := nodehost.OutputRecords{Bind: func(context.Context, nodehost.NodeInput) (nodehost.JobUpload, error) {
			return nodehost.JobUpload{Identity: identity, Authority: authority, Key: blob.ResultRecordKey(attempt, authority.Repo, authority.Ref.Scope, "fixture", blob.KindOutput)}, nil
		}}
		key, writeErr := writer.PublishOutput(ctx, input, body)
		if writeErr != nil {
			return fmt.Errorf("store host output: %w", writeErr)
		}
		hit, readErr := data.Records.LookupRecord(ctx, blob.RecordLookup{Authority: authority, Key: *key, Budget: data.Budget})
		if readErr != nil || !hit.Found {
			return fmt.Errorf("read host output record: %v", readErr)
		}
		result = hit.Record
	}
	if err != nil {
		return fmt.Errorf("store physical output: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "lockfile"), seed[:], 0o600); err != nil {
		return err
	}
	key, err := depcachekey.Files(root, "modules", "", []string{"lockfile"})
	if err != nil {
		return err
	}
	source := filepath.Join(root, "producer")
	if err := os.Mkdir(source, 0o700); err != nil {
		return err
	}
	payload := bytes.Repeat(seed[:], 1+int(seed[0]))
	if err := os.WriteFile(filepath.Join(source, "module"), payload, 0o640); err != nil {
		return err
	}
	artifactData := artifacts.Data{Bytes: data.Bytes, Records: data.Records, Authority: authority, Identity: identity, Pipeline: "fixture", Budget: data.Budget}
	publisher := nodehost.ArtifactRecords{Bind: func(context.Context, nodehost.NodeInput) (nodehost.JobUpload, error) {
		return nodehost.JobUpload{Identity: identity, Authority: authority, Key: blob.ResultRecordKey(attempt, authority.Repo, authority.Ref.Scope, "fixture", blob.KindManifest)}, nil
	}}
	manifestKey, err := publisher.Publish(ctx, input, source, []string{"module"}, func(string) {})
	if err != nil {
		return fmt.Errorf("store host artifacts: %w", err)
	}
	manifestHit, err := data.Records.LookupRecord(ctx, blob.RecordLookup{Authority: authority, Key: *manifestKey, Budget: data.Budget})
	if err != nil || !manifestHit.Found {
		return fmt.Errorf("read physical artifact manifest: %v", err)
	}
	manifest := manifestHit.Record
	backend := depcacheblob.New(data)
	if _, err := backend.Store(ctx, key, source); err != nil {
		return fmt.Errorf("save physical archive: %w", err)
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, dataController, err := openStorageData(path, objects, now)
	if err != nil {
		return err
	}
	s = reopened
	binding, err = bindStorageData(ctx, s, dataController, identity.CapabilityRef)
	if err != nil {
		return err
	}
	if replay, err := binding.Signer.Commit(ctx, settlement); err != nil || replay != receipt {
		return fmt.Errorf("reopened upload settlement replay differs: %+v: %v", replay, err)
	}
	data.Bytes = blob.NewSignedStore(blob.SignedStoreOptions{Signer: binding.Signer, HTTPClient: transfers})
	data.Records = binding.Records
	output.Bytes = data.Bytes
	output.Records = data.Records
	if got, err := output.Read(ctx, result.Key); err != nil || !bytes.Equal(got, body) {
		return fmt.Errorf("reopened physical output differs: %v", err)
	}
	artifactData.Bytes, artifactData.Records = data.Bytes, data.Records
	if err := os.Mkdir(filepath.Join(root, "staged"), 0o700); err != nil {
		return err
	}
	if _, err := (nodehost.ArtifactRecords{Blobs: artifactData.Bytes, Records: artifactData.Records}).Stage(ctx, blob.RecordLookup{Authority: artifactData.Authority, Key: manifest.Key, Budget: artifactData.Budget}, filepath.Join(root, "staged"), ""); err != nil {
		return fmt.Errorf("stage physical artifacts: %w", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "staged", "module")); err != nil || !bytes.Equal(got, payload) {
		return fmt.Errorf("staged physical artifact differs: %v", err)
	}
	backend = depcacheblob.New(data)
	destination := filepath.Join(root, "deps")
	if _, err := backend.Fetch(ctx, key, destination); err != nil {
		return fmt.Errorf("restore reopened archive: %w", err)
	}
	if body, err := os.ReadFile(filepath.Join(destination, "module")); err != nil || !bytes.Equal(body, payload) {
		return fmt.Errorf("reopened archive differs: %v", err)
	}
	if err := checkDepcacheScopes(ctx, data, key, destination, seed); err != nil {
		return err
	}
	lookup := blob.DepcacheLookup{Authority: authority, Key: blob.DepcacheKey{Repo: authority.Repo, Ref: authority.Ref.Scope, Name: key, Key: key}, Budget: data.Budget}
	hit, err := data.Records.LookupDepcache(ctx, lookup)
	if err != nil || !hit.Found {
		return fmt.Errorf("saved archive record missing: %v", err)
	}
	objectKey, err := blob.ObjectKey(identity.Team, hit.Record.Digest)
	if err != nil {
		return err
	}
	if err := corrupt(objectKey); err != nil {
		return err
	}
	if _, err := backend.Fetch(ctx, key, destination); err == nil {
		return errors.New("corrupt archive restored")
	}
	if body, err := os.ReadFile(filepath.Join(destination, "module")); err != nil || !bytes.Equal(body, payload) {
		return fmt.Errorf("failed archive read replaced destination: %v", err)
	}
	if _, err := teststore.SQL(t, s).ExecContext(ctx, "UPDATE capabilities SET revoked_at=? WHERE prefix=?", teststore.Now(s).UnixNano(), identity.CapabilityRef); err != nil {
		return err
	}
	if _, err := binding.Signer.Commit(ctx, settlement); err == nil {
		return errors.New("revoked writer replayed settled upload")
	}
	if _, err := binding.Records.LookupRecord(ctx, blob.RecordLookup{Authority: authority, Key: result.Key}); err == nil {
		return errors.New("revoked writer read output record")
	}
	return nil
}

func checkDepcacheScopes(ctx context.Context, data depcacheblob.Data, key, destination string, seed [16]byte) error {
	for _, field := range []string{"repo", "attempt", "ref"} {
		bad := data
		switch field {
		case "repo":
			bad.Authority.Repo += runspec.RepoKey(fmt.Sprintf("-%x", seed))
		case "attempt":
			bad.Authority.Attempt.Value.Generation++
		case "ref":
			bad.Authority.Ref.Scope.Verified = true
		}
		if _, err := depcacheblob.New(bad).Fetch(ctx, key, destination); err == nil {
			return fmt.Errorf("forged %s restored an archive", field)
		}
	}
	return nil
}

func seedDepcacheBoundary(t testing.TB, ctx context.Context, s *store.Store) (blob.Authority, blob.UploadIdentity, error) {
	commit, err := seedStoreLogBoundary(ctx, teststore.SQL(t, s), teststore.Now(s))
	if err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	return seedStorageJobClaim(t, ctx, s, commit)
}

func seedStorageJobClaim(t testing.TB, ctx context.Context, s *store.Store, commit store.SegmentCommit) (blob.Authority, blob.UploadIdentity, error) {
	var raw []byte
	if err := teststore.SQL(t, s).QueryRowContext(ctx, "SELECT authority_json FROM capabilities WHERE prefix=?", commit.Actor.CapabilityPrefix).Scan(&raw); err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	var envelope store.CapabilityRecord
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	c := &envelope.Value
	c.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}
	c.Prefix = "archive-claim"
	c.Digest = sha256.Sum256([]byte(c.Prefix))
	c.Grants = []capability.Grant{capability.DataRead, capability.DataWrite}
	c.Issuance, c.Unit, c.Purpose, c.Delivery = capability.Award, capability.UnitNode, capability.PurposeWork, capability.DeliveryDirect
	unit, err := json.Marshal(store.UnitRef{Kind: c.Unit, RunID: runspec.RunID(c.Binding.RunID), NodeID: runspec.NodeID(c.Binding.NodeID)})
	if err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	c.UnitID = string(unit)
	c.Billing = &capability.BillingProvenance{PayerTeam: c.Team, Unit: capability.BillingJob, Start: capability.BillingAtStart, ExecutorClass: "runner", ReservationID: "archive-proof"}
	c.Data = &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "gh:1", RunID: c.Binding.RunID, NodeID: c.Binding.NodeID, Attempt: 1, Generation: c.Binding.Generation, WriteRef: fixtureMainRef.CapabilityScope(), ReadRefs: []string{fixtureMainRef.CapabilityScope()}}
	if err := envelope.Validate(); err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	raw, err = json.Marshal(envelope)
	if err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	grants, err := json.Marshal(c.Grants)
	if err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	_, err = teststore.SQL(t, s).ExecContext(ctx, `INSERT INTO capabilities(prefix,digest,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,bind_run,bind_node,bind_generation,bind_holder,reserved_pools,created_at,created_by,repo_scope,parent_kind,parent_id,award_purpose,delivery_purpose,unit_id,billing_unit,payer_team,executor_class,reservation_id,billing_start,unit_kind,repo_key,ref_scope)
SELECT ?,?,team,subject_kind,subject_id,audience,?,'award',1,?,bind_kind,bind_run,bind_node,bind_generation,bind_holder,reserved_pools,created_at,created_by,repo_scope,'capability',prefix,'work','direct',?,'job',team,'runner','archive-proof','at_start','node','gh:1','manual:account/fixture:main' FROM capabilities WHERE prefix=?`, c.Prefix, hex.EncodeToString(c.Digest[:]), grants, raw, c.UnitID, c.Parent.ID)
	if err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	for _, table := range []string{"nodes", "node_attempts"} {
		if _, err := teststore.SQL(t, s).ExecContext(ctx, "UPDATE "+table+" SET capability_prefix=? WHERE team=? AND run_id=?", c.Prefix, c.Team, c.Binding.RunID); err != nil {
			return blob.Authority{}, blob.UploadIdentity{}, err
		}
	}
	if _, err := teststore.SQL(t, s).ExecContext(ctx, "UPDATE runs SET request_json=? WHERE team=? AND id=?", []byte(`{"version":1,"request":{"git":{"ref":"main"}},"provenance":{"trust":"asserted","ref":"main","pipeline":"ci","writer":"account/fixture"}}`), c.Team, c.Binding.RunID); err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	if _, err := teststore.SQL(t, s).ExecContext(ctx, "UPDATE nodes SET spec_json=? WHERE team=? AND run_id=?", []byte(`{"memo":{}}`), c.Team, c.Binding.RunID); err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	attempt := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: *commit.Subject.Attempt}
	identity := blob.UploadIdentity{Kind: blob.UploadJob, Team: c.Team, RunID: c.Binding.RunID, Principal: blob.Principal(c.Subject.ID), CapabilityRef: c.Prefix, Attempt: attempt}
	authority := blob.Authority{CapabilityRef: c.Prefix, Principal: identity.Principal, Attempt: attempt, Repo: "gh:1", Ref: blob.RefFacts{Scope: fixtureMainRef}}
	return authority, identity, nil
}

func openStorageData(path string, objects blob.ObjectBackend, now time.Time) (*store.Store, *controller.DataController, error) {
	var data *controller.DataController
	s, _, err := openControllerFixture(path, func(s *store.Store) *controller.Server {
		data = controller.NewDataController(s, controller.DataController{Objects: objects})
		return controller.New(s, nil).WithDataController(data)
	}, teststore.WithClock(func() time.Time { return now }))
	return s, data, err
}

func bindStorageData(ctx context.Context, s *store.Store, data *controller.DataController, prefix string) (controller.DataBinding, error) {
	p, err := s.AsOperator().ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
	if err != nil {
		return controller.DataBinding{}, err
	}
	return data.BindData(ctx, capability.Authorized{
		Principal: p, Binding: p.Capability.Binding, Target: capability.Target{Team: p.Capability.Team},
		Access: capability.AccessRule{Credential: capability.CredentialClaim, Grants: p.Capability.Grants, Binding: capability.BindData, Liveness: capability.LiveSensitive},
	})
}

func settleStorageProbe(ctx context.Context, binding controller.DataBinding, objects blob.ObjectBackend, transfers blob.HTTPClient, body []byte) (blob.CommitUpload, blob.Receipt, error) {
	authority := binding.Authority
	intent := blob.UploadIntent{Authority: authority, Identity: authority.Upload.Value, Digest: storageDigest(body), Size: int64(len(body))}
	forged := intent
	forged.Identity.Attempt.Value.Generation++
	if _, err := binding.Signer.Upload(ctx, forged); err == nil {
		return blob.CommitUpload{}, blob.Receipt{}, errors.New("forged uploader obtained a ticket")
	}
	ticket, err := binding.Signer.Upload(ctx, intent)
	if err != nil {
		return blob.CommitUpload{}, blob.Receipt{}, err
	}
	request := blob.CommitUpload{Authority: authority, TicketID: ticket.ID}
	if _, err := binding.Signer.Commit(ctx, request); !errors.Is(err, blob.ErrNotFound) {
		return request, blob.Receipt{}, fmt.Errorf("unverified ticket settled: %v", err)
	}
	response, err := transfers.Transfer(ctx, ticket.Transfer, bytes.NewReader(body))
	if response != nil {
		err = errors.Join(err, response.Close())
	}
	if err != nil {
		return request, blob.Receipt{}, err
	}
	receipt, err := binding.Signer.Commit(ctx, request)
	if err != nil || receipt.Identity != intent.Identity || receipt.Digest != intent.Digest {
		return request, receipt, fmt.Errorf("verified ticket settlement differs: %+v: %v", receipt, err)
	}
	key := "teams/" + intent.Identity.Team + "/pending/" + ticket.ID
	if err := objects.CommitObject(ctx, key, blob.Blob{Team: intent.Identity.Team, Digest: intent.Digest, Size: intent.Size}); !errors.Is(err, blob.ErrNotFound) {
		return request, receipt, fmt.Errorf("settlement retained staging: %v", err)
	}
	if replay, err := binding.Signer.Commit(ctx, request); err != nil || replay != receipt {
		return request, receipt, fmt.Errorf("settlement replay differs: %+v: %v", replay, err)
	}
	return request, receipt, nil
}
