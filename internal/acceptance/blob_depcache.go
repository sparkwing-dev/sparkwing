package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	depcacheblob "github.com/sparkwing-dev/sparkwing/internal/blob/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/depcachekey"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type depcacheBoundarySigner struct {
	blob.DataSigner
	s       *store.Store
	objects blob.ObjectBackend
	next    int
}

func (s *depcacheBoundarySigner) tenant(ctx context.Context) (*store.Tenant, error) {
	return s.s.ForTeam(ctx, store.DefaultTeam)
}

func (s *depcacheBoundarySigner) Upload(ctx context.Context, intent blob.UploadIntent) (blob.UploadTicket, error) {
	tenant, err := s.tenant(ctx)
	if err != nil {
		return blob.UploadTicket{}, err
	}
	s.next++
	ticket, err := tenant.ReserveBlobUpload(ctx, store.UploadReservationRequest{ID: fmt.Sprintf("archive-%d", s.next), Intent: intent, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		return blob.UploadTicket{}, err
	}
	object, err := s.objects.Sign(ctx, capability.ObjectToSign{Method: http.MethodPut, Key: ticket.StagingKey, Size: ticket.Blob.Size, SHA256: string(ticket.Blob.Digest), ExpiresAt: ticket.ExpiresAt})
	if err != nil {
		return blob.UploadTicket{}, errors.Join(err, tenant.AbortBlobUpload(ctx, intent.Authority, ticket.ID))
	}
	return blob.UploadTicket{ID: ticket.ID, Transfer: boundaryTransfer(object)}, nil
}

func (s *depcacheBoundarySigner) Commit(ctx context.Context, request blob.CommitUpload) (blob.Receipt, error) {
	tenant, err := s.tenant(ctx)
	if err != nil {
		return blob.Receipt{}, err
	}
	ticket, err := tenant.LookupBlobUpload(ctx, request.Authority, request.TicketID)
	if err != nil {
		return blob.Receipt{}, err
	}
	if err := s.objects.CommitObject(ctx, ticket.StagingKey, ticket.Blob); err != nil {
		return blob.Receipt{}, err
	}
	receipt, err := tenant.CommitBlobUpload(ctx, store.VerifiedUploadCommit{Authority: request.Authority, TicketID: ticket.ID, Verified: ticket.Blob, ReplicaID: "fixture"})
	if err != nil {
		return blob.Receipt{}, err
	}
	return receipt, s.objects.DeleteObject(ctx, ticket.StagingKey)
}

func (s *depcacheBoundarySigner) Download(ctx context.Context, request blob.GetRequest) (blob.SignedTransfer, error) {
	tenant, err := s.tenant(ctx)
	if err != nil {
		return blob.SignedTransfer{}, err
	}
	presence, err := tenant.ReadableBlobs(ctx, blob.HasManyRequest{Authority: request.Authority, Digests: []blob.Digest{request.Digest}})
	if err != nil {
		return blob.SignedTransfer{}, err
	}
	if len(presence.Present) != 1 {
		return blob.SignedTransfer{}, blob.ErrDigestNotReadable
	}
	var size int64
	if err := s.s.DB().QueryRowContext(ctx, "SELECT size FROM blobs WHERE team=? AND digest=?", store.DefaultTeam, request.Digest).Scan(&size); err != nil {
		return blob.SignedTransfer{}, err
	}
	key, err := blob.ObjectKey(string(store.DefaultTeam), request.Digest)
	if err != nil {
		return blob.SignedTransfer{}, err
	}
	object, err := s.objects.Sign(ctx, capability.ObjectToSign{Method: http.MethodGet, Key: key, Size: size, SHA256: string(request.Digest), ExpiresAt: time.Now().Add(time.Minute)})
	return boundaryTransfer(object), err
}

func boundaryTransfer(object capability.SignedObject) blob.SignedTransfer {
	return blob.SignedTransfer{URL: object.URL, Headers: object.Headers, Size: object.Size, Digest: blob.Digest(object.SHA256), ExpiresAt: object.ExpiresAt}
}

// CheckBlobDepcache joins archive recovery to physical transfers and persisted upload/record transactions.
func CheckBlobDepcache(ctx context.Context, root string, objects blob.ObjectBackend, transfers blob.HTTPClient, corrupt func(string) error, seed [16]byte) (err error) {
	path := filepath.Join(root, "archives.db")
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	authority, identity, err := seedDepcacheBoundary(ctx, s)
	if err != nil {
		return err
	}
	signer := &depcacheBoundarySigner{s: s, objects: objects}
	data := depcacheblob.Data{Bytes: blob.NewSignedStore(blob.SignedStoreOptions{Signer: signer, HTTPClient: transfers}), Authority: authority, Identity: identity, Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	data.Records = tenant.BlobRecords(storage.BlobManifests{Objects: objects})
	output := blob.OutputData{Bytes: data.Bytes, Records: data.Records, Authority: authority, Identity: identity, Pipeline: "fixture", Budget: data.Budget}
	body := seed[:]
	if seed[0]%2 == 0 {
		body = nil
	}
	result, err := output.Put(ctx, body)
	if err != nil {
		return fmt.Errorf("store physical output: %w", err)
	}
	memoKey, err := blob.ResolveWriteKey(authority, blob.WriteScopeIntent{Kind: blob.KindMemo, Pipeline: "fixture", Job: identity.Attempt.Value.NodeID, Key: fmt.Sprintf("%x", sha256.Sum256(seed[:])), Name: blob.MemoRecordName})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "lockfile"), seed[:], 0o600); err != nil {
		return err
	}
	spec := depcache.Spec{Name: "modules", Path: "deps", KeyFiles: []string{"lockfile"}}
	key, err := depcachekey.Files(root, spec.Name, "", spec.KeyFiles)
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
	manifest, err := artifacts.Capture(ctx, artifactData, source, []string{"module"}, func(string) {})
	if err != nil {
		return fmt.Errorf("store physical artifacts: %w", err)
	}
	manifestHit, err := data.Records.LookupRecord(ctx, blob.RecordLookup{Authority: authority, Key: manifest.Key, Budget: data.Budget})
	if err != nil || !manifestHit.Found {
		return fmt.Errorf("read physical artifact manifest: %v", err)
	}
	if _, err := data.Records.CommitRecord(ctx, blob.CommitRecordRequest{Authority: authority, Source: identity, Key: memoKey, Digest: result.Digest, ManifestDigest: runspec.Optional[blob.Digest]{Set: true, Value: manifest.Digest}, Members: manifestHit.Members}); err != nil {
		return fmt.Errorf("store physical memo: %w", err)
	}
	backend := depcacheblob.New(data)
	if _, err := backend.Store(ctx, key, source); err != nil {
		return fmt.Errorf("save physical archive: %w", err)
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := store.Open(path)
	if err != nil {
		return err
	}
	s, signer.s = reopened, reopened
	tenant, err = s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	data.Records = tenant.BlobRecords(storage.BlobManifests{Objects: objects})
	output.Records = data.Records
	if got, err := output.Read(ctx, result.Key); err != nil || !bytes.Equal(got, body) {
		return fmt.Errorf("reopened physical output differs: %v", err)
	}
	hitMemo, err := data.Records.LookupMemo(ctx, blob.MemoLookup{Authority: authority, Key: blob.MemoKey{Repo: authority.Repo, Ref: authority.Ref.Scope, Pipeline: "fixture", Job: identity.Attempt.Value.NodeID, Hash: runspec.Hash(memoKey.Scope.Key)}, Budget: data.Budget})
	if err != nil || !hitMemo.Found || !hitMemo.Output.Set || hitMemo.Output.Value != result.Digest {
		return fmt.Errorf("reopened physical memo differs: %+v: %v", hitMemo, err)
	}
	copied, err := blob.MaterializeMemo(ctx, data.Bytes, data.Records, blob.CopyMemoRequest{Lookup: blob.MemoLookup{Authority: authority, Key: blob.MemoKey{Repo: authority.Repo, Ref: authority.Ref.Scope, Pipeline: "fixture", Job: identity.Attempt.Value.NodeID, Hash: runspec.Hash(memoKey.Scope.Key)}, Budget: data.Budget}, Destination: identity.Attempt.Value})
	if err != nil || !copied.Hit.Found || !bytes.Equal(copied.Output, body) || !copied.Hit.Manifest.Set || copied.Hit.Manifest.Value != manifest.Digest {
		return fmt.Errorf("materialize physical memo: %+v: %v", copied, err)
	}
	artifactData.Records = data.Records
	if err := os.Mkdir(filepath.Join(root, "staged"), 0o700); err != nil {
		return err
	}
	if _, err := artifacts.Stage(ctx, artifactData, filepath.Join(root, "staged"), []artifacts.Edge{{Record: manifest.Key}}); err != nil {
		return fmt.Errorf("stage physical memo artifacts: %w", err)
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
	return checkDepcacheColdSave(ctx, data, root, spec, key, hit.Record.Digest, append(bytes.Clone(payload), seed[:]...), objects)
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

func checkDepcacheColdSave(ctx context.Context, data depcacheblob.Data, root string, spec depcache.Spec, key string, previous blob.Digest, recovered []byte, objects blob.ObjectBackend) error {
	destination := filepath.Join(root, spec.Path)
	backend := depcacheblob.New(data)
	authority := data.Authority
	lookup := blob.DepcacheLookup{Authority: authority, Key: blob.DepcacheKey{Repo: authority.Repo, Ref: authority.Ref.Scope, Name: key, Key: key}, Budget: data.Budget}
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	cacheCtx := depcache.WithBackend(ctx, backend)
	run := depcache.Restore(cacheCtx, spec, "job", root)
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("corrupt archive did not run cold: %v", err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(destination, "module"), recovered, 0o640); err != nil {
		return err
	}
	run.Save(cacheCtx, errors.New("body failed"))
	afterFailure, err := data.Records.LookupDepcache(ctx, lookup)
	if err != nil || !afterFailure.Found || afterFailure.Record.Digest != previous {
		return fmt.Errorf("failed execution changed cache record: %+v: %v", afterFailure, err)
	}
	run.Save(cacheCtx, nil)
	if _, err := backend.Fetch(ctx, key, filepath.Join(root, "recovered")); err != nil {
		return fmt.Errorf("cold execution did not repair archive: %w", err)
	}
	if body, err := os.ReadFile(filepath.Join(root, "recovered", "module")); err != nil || !bytes.Equal(body, recovered) {
		return fmt.Errorf("repaired archive differs: %v", err)
	}
	hit, err := data.Records.LookupDepcache(ctx, lookup)
	if err != nil || !hit.Found || hit.Record.Digest == afterFailure.Record.Digest {
		return fmt.Errorf("successful execution did not replace corrupt cache: %+v: %v", hit, err)
	}
	if err := objects.DeleteBlob(ctx, blob.Blob{Team: data.Identity.Team, Digest: hit.Record.Digest}); err != nil {
		return err
	}
	if _, err := backend.Fetch(ctx, key, destination); err == nil {
		return errors.New("missing archive restored")
	}
	if body, err := os.ReadFile(filepath.Join(destination, "module")); err != nil || !bytes.Equal(body, recovered) {
		return fmt.Errorf("missing archive replaced destination: %v", err)
	}
	return nil
}

func seedDepcacheBoundary(ctx context.Context, s *store.Store) (blob.Authority, blob.UploadIdentity, error) {
	commit, err := seedStoreLogBoundary(ctx, s.DB())
	if err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	var raw []byte
	if err := s.DB().QueryRowContext(ctx, "SELECT authority_json FROM capabilities WHERE prefix=?", commit.Actor.CapabilityPrefix).Scan(&raw); err != nil {
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
	unit, err := json.Marshal(store.UnitRef{Kind: c.Unit, RunID: c.Binding.RunID, NodeID: c.Binding.NodeID})
	if err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	c.UnitID = string(unit)
	c.Billing = &capability.BillingProvenance{PayerTeam: c.Team, Unit: capability.BillingJob, Start: capability.BillingAtStart, ExecutorClass: "runner", ReservationID: "archive-proof"}
	c.Data = &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "gh:1", RunID: c.Binding.RunID, NodeID: c.Binding.NodeID, Attempt: 1, Generation: c.Binding.Generation, WriteRef: "manual:main", ReadRefs: []string{"manual:main"}}
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
	_, err = s.DB().ExecContext(ctx, `INSERT INTO capabilities(prefix,digest,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,bind_run,bind_node,bind_generation,bind_holder,reserved_pools,created_at,created_by,repo_scope,parent_kind,parent_id,award_purpose,delivery_purpose,unit_id,billing_unit,payer_team,executor_class,reservation_id,billing_start,unit_kind,repo_key,ref_scope)
SELECT ?,?,team,subject_kind,subject_id,audience,?,'award',1,?,bind_kind,bind_run,bind_node,bind_generation,bind_holder,reserved_pools,created_at,created_by,repo_scope,'capability',prefix,'work','direct',?,'job',team,'runner','archive-proof','at_start','node','gh:1','manual:main' FROM capabilities WHERE prefix=?`, c.Prefix, hex.EncodeToString(c.Digest[:]), grants, raw, c.UnitID, c.Parent.ID)
	if err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	for _, table := range []string{"nodes", "node_attempts"} {
		if _, err := s.DB().ExecContext(ctx, "UPDATE "+table+" SET capability_prefix=? WHERE team=? AND run_id=?", c.Prefix, c.Team, c.Binding.RunID); err != nil {
			return blob.Authority{}, blob.UploadIdentity{}, err
		}
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE runs SET request_json=? WHERE team=? AND id=?", []byte(`{"version":1,"request":{"git":{"ref":"main"}}}`), c.Team, c.Binding.RunID); err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE nodes SET spec_json=? WHERE team=? AND run_id=?", []byte(`{"memo":{}}`), c.Team, c.Binding.RunID); err != nil {
		return blob.Authority{}, blob.UploadIdentity{}, err
	}
	attempt := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: *commit.Subject.Attempt}
	identity := blob.UploadIdentity{Kind: blob.UploadJob, Team: c.Team, RunID: c.Binding.RunID, Principal: blob.Principal(c.Subject.ID), CapabilityRef: c.Prefix, Attempt: attempt}
	authority := blob.Authority{CapabilityRef: c.Prefix, Principal: identity.Principal, Attempt: attempt, Repo: "gh:1", Ref: blob.RefFacts{Scope: blob.RefScope{Ref: "main"}}}
	return authority, identity, nil
}
