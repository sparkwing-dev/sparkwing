package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// BlobManifestReader decodes members only after reading and verifying the manifest digest.
// The SQL member table stores reachability; paths and modes remain in immutable manifest bytes.
type BlobManifestReader interface {
	ReadBlobManifest(context.Context, Team, blob.Digest) ([]blob.Member, error)
}

type blobRecordRules interface {
	RecordNamespace(blob.RecordKey) (string, error)
	AuthorizeEdge(blob.EdgeRequest) error
}

type sharedBlobRules struct{}

func (sharedBlobRules) RecordNamespace(key blob.RecordKey) (string, error) {
	return blob.RecordNamespace(key)
}
func (sharedBlobRules) AuthorizeEdge(edge blob.EdgeRequest) error { return blob.AuthorizeEdge(edge) }

type blobRecords struct {
	tenant    *Tenant
	manifests BlobManifestReader
	rules     blobRecordRules
}

// BlobRecords binds transactional records to the controller's verified manifest reader.
// Output-only consumers may omit the reader; manifest operations then fail explicitly.
func (t *Tenant) BlobRecords(manifests BlobManifestReader) blob.RecordStore {
	return &blobRecords{tenant: t, manifests: manifests, rules: sharedBlobRules{}}
}

func (r *blobRecords) manifestMembers(ctx context.Context, request blob.CommitRecordRequest) ([]blob.Member, error) {
	manifest := request.ManifestDigest
	if request.Key.Kind == blob.KindManifest {
		manifest = runspec.Optional[blob.Digest]{Set: true, Value: request.Digest}
	}
	if !manifest.Set {
		if len(request.Members) > 0 {
			return nil, authorityError(apierr.BadRequest, "members require a manifest")
		}
		return nil, nil
	}
	if r.manifests == nil {
		return nil, authorityError(apierr.Unavailable, "verified manifest reader is not configured")
	}
	members, err := r.manifests.ReadBlobManifest(ctx, r.tenant.team, manifest.Value)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(members, request.Members) && !(len(members) == 0 && len(request.Members) == 0) {
		return nil, blob.ErrIntegrity
	}
	return members, nil
}

func (r *blobRecords) CommitRecord(ctx context.Context, request blob.CommitRecordRequest) (blob.Record, error) {
	if request.ExpiresAt.Set {
		request.ExpiresAt.Value = request.ExpiresAt.Value.UTC()
	} else {
		request.ExpiresAt.Value = time.Time{}
	}
	if !request.ManifestDigest.Set {
		request.ManifestDigest.Value = ""
	}
	tx, err := r.tenant.s.beginTx(ctx)
	if err != nil {
		return blob.Record{}, err
	}
	defer rollbackOrLog(tx)
	a, err := resolveBlobAuthorityTx(ctx, tx, r.tenant.team, request.Authority, capability.DataWrite)
	if err != nil {
		return blob.Record{}, err
	}
	if !reflect.DeepEqual(request.Source, a.source) || !blobKindAllowed(a.source, request.Key.Kind) {
		return blob.Record{}, authorityError(apierr.Forbidden, "record is outside the uploader's authority")
	}
	if request.Key.Kind == blob.KindManifest && request.ManifestDigest.Set && request.ManifestDigest.Value != request.Digest {
		return blob.Record{}, authorityError(apierr.BadRequest, "manifest record identifies two manifests")
	}
	if err := checkBlobWriteKey(a, request.Key); err != nil {
		return blob.Record{}, err
	}
	if request.Key.Kind == blob.KindMemo {
		if err := requireBlobMemoTx(ctx, tx, a); err != nil {
			return blob.Record{}, err
		}
	}
	if err := r.authorizeDigestTx(ctx, tx, a, request.Key.Kind, request.Digest); err != nil {
		return blob.Record{}, err
	}
	if request.ManifestDigest.Set {
		if err := r.authorizeDigestTx(ctx, tx, a, request.Key.Kind, request.ManifestDigest.Value); err != nil {
			return blob.Record{}, err
		}
	}
	members, err := r.manifestMembers(ctx, request)
	if err != nil {
		return blob.Record{}, err
	}
	scope, err := r.rules.RecordNamespace(request.Key)
	if err != nil {
		return blob.Record{}, err
	}
	if request.ExpiresAt.Set && !request.ExpiresAt.Value.After(tx.now()) {
		return blob.Record{}, authorityError(apierr.BadRequest, "record expiry is not in the future")
	}
	if request.Key.Kind == blob.KindOutput || (request.Key.Kind == blob.KindMemo && request.Key.Name == "output") {
		size, err := requireBlobTx(ctx, tx, r.tenant.team, request.Digest)
		if err != nil {
			return blob.Record{}, err
		}
		if size > limits.NodeOutputBytes {
			return blob.Record{}, authorityError(apierr.PayloadTooLarge, "output exceeds the byte limit")
		}
	}
	if request.Key.Kind == blob.KindMemo {
		if !request.ExpiresAt.Set {
			request.ExpiresAt = runspec.Optional[time.Time]{Set: true, Value: tx.now().Add(limits.MemoDefaultTTL)}
		}
		if request.ExpiresAt.Value.After(tx.now().Add(limits.MemoMaxTTL)) {
			return blob.Record{}, authorityError(apierr.BadRequest, "memo expiry exceeds the maximum TTL")
		}
		if request.Key.Name == "manifest" && (!request.ManifestDigest.Set || request.ManifestDigest.Value != request.Digest) {
			return blob.Record{}, authorityError(apierr.BadRequest, "manifest-only memo must identify its manifest")
		}
	}
	record := blob.Record{Key: request.Key, Digest: request.Digest, ManifestDigest: request.ManifestDigest, Source: a.source, Uploader: a.authority.Principal, Provenance: blobActorProvenance(a), RefFacts: a.authority.Ref, CreatedAt: tx.now(), ExpiresAt: request.ExpiresAt}
	batch, err := r.mutateRecordsTx(ctx, tx, a, []blobRecordMutation{{record: record, scope: scope, members: members}}, false)
	if err != nil {
		return blob.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return blob.Record{}, err
	}
	return batch[0], nil
}

func checkBlobWriteKey(a blobActor, key blob.RecordKey) error {
	if key.Team != a.capability.Team || key.Scope.Repo != a.authority.Repo || key.Scope.Ref != a.authority.Ref.Scope || (key.Kind != blob.KindDepcache && key.Scope.Pipeline != a.pipeline) || key.Name == "" || !slices.Contains(blob.Kinds, key.Kind) {
		return authorityError(apierr.Forbidden, "record key differs from sealed scope")
	}
	if blobIsCache(key.Kind) && blobLocalRepo(key.Scope.Repo) {
		return authorityError(apierr.Forbidden, "shared caches require a remote repository")
	}
	if a.source.Attempt.Set && key.Kind != blob.KindDepcache && key.Scope.Job != a.source.Attempt.Value.NodeID {
		return authorityError(apierr.Forbidden, "record names another job")
	}
	if !a.source.Attempt.Set && key.Scope.Job != "" {
		return authorityError(apierr.Forbidden, "owner upload cannot name a job")
	}
	if key.Kind == blob.KindDepcache && (key.Scope.Job != "" || key.Scope.Pipeline != "") {
		return authorityError(apierr.BadRequest, "depcache namespace is repository-scoped")
	}
	if key.Kind == blob.KindMemo && key.Name != "output" && key.Name != "manifest" {
		return authorityError(apierr.BadRequest, "memo record must name output or manifest")
	}
	if key.Kind == blob.KindMemo && key.Scope.Key == "" {
		return authorityError(apierr.BadRequest, "memo record requires an input hash")
	}
	if key.Kind == blob.KindOutput || key.Kind == blob.KindManifest {
		if key != blob.ResultRecordKey(a.source.Attempt.Value, a.authority.Repo, a.authority.Ref.Scope, a.pipeline, key.Kind) {
			return authorityError(apierr.Forbidden, "result key differs from attempt fence")
		}
	}
	if a.capability.Data.WriteRef != blobRefScope(key.Scope.Ref) {
		return authorityError(apierr.Forbidden, "record ref is outside the data grant")
	}
	return nil
}

func persistBlobRecordTx(ctx context.Context, tx *storeTx, record blob.Record, scope string, destination blob.UploadIdentity) error {
	sourceJSON, err := json.Marshal(record.Source)
	if err != nil {
		return err
	}
	factsJSON, err := json.Marshal(record.RefFacts)
	if err != nil {
		return err
	}
	var manifest, run, node, attempt, generation, sourceNode, sourceAttempt, sourceGeneration, expires any
	if record.ManifestDigest.Set {
		manifest = record.ManifestDigest.Value
	}
	if record.ExpiresAt.Set {
		expires = record.ExpiresAt.Value.UnixNano()
	}
	if record.Source.Attempt.Set {
		a := record.Source.Attempt.Value
		sourceNode, sourceAttempt, sourceGeneration = a.NodeID, a.Attempt, a.Generation
	}
	if record.Key.Kind == blob.KindOutput || record.Key.Kind == blob.KindManifest {
		run = destination.RunID
		a := destination.Attempt.Value
		node, attempt, generation = a.NodeID, a.Attempt, a.Generation
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO blob_records(team,kind,scope,name,digest,manifest_digest,run_id,node_id,attempt,generation,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,source_node_id,source_attempt,source_generation,verified_ref,verified_default_branch,source_sha,ref_facts_json,pipeline,created_at,expires_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(team,kind,scope,name) DO UPDATE SET digest=excluded.digest,manifest_digest=excluded.manifest_digest,run_id=excluded.run_id,node_id=excluded.node_id,attempt=excluded.attempt,generation=excluded.generation,uploader_principal=excluded.uploader_principal,provenance=excluded.provenance,source_identity_json=excluded.source_identity_json,source_kind=excluded.source_kind,source_run_id=excluded.source_run_id,source_node_id=excluded.source_node_id,source_attempt=excluded.source_attempt,source_generation=excluded.source_generation,verified_ref=excluded.verified_ref,verified_default_branch=excluded.verified_default_branch,source_sha=excluded.source_sha,ref_facts_json=excluded.ref_facts_json,created_at=excluded.created_at,expires_at=excluded.expires_at`, record.Key.Team, record.Key.Kind, scope, record.Key.Name, record.Digest, manifest, run, node, attempt, generation, record.Key.Scope.Repo, blobRefScope(record.Key.Scope.Ref), record.Uploader, record.Provenance, sourceJSON, record.Source.Kind, record.Source.RunID, sourceNode, sourceAttempt, sourceGeneration, boolInt(record.RefFacts.Scope.Verified), boolInt(record.RefFacts.DefaultBranchVerified), record.RefFacts.SHA, factsJSON, record.Key.Scope.Pipeline, record.CreatedAt.UnixNano(), expires)
	return err
}

func persistBlobMembersTx(ctx context.Context, tx *storeTx, team Team, record blob.Record, members []blob.Member) error {
	manifest := record.ManifestDigest.Value
	if record.Key.Kind == blob.KindManifest {
		manifest = record.Digest
	}
	for _, member := range members {
		if member.Team != string(team) || member.ManifestDigest != manifest {
			return authorityError(apierr.BadRequest, "manifest member identity differs")
		}
		size, err := requireBlobTx(ctx, tx, team, member.Digest)
		if err != nil {
			return err
		}
		if size != member.Size {
			return blob.ErrIntegrity
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO blob_members(team,manifest_digest,member_digest) VALUES (?,?,?) ON CONFLICT(team,manifest_digest,member_digest) DO NOTHING", team, manifest, member.Digest); err != nil {
			return err
		}
	}
	return nil
}

func readBlobRecordTx(ctx context.Context, tx *storeTx, key blob.RecordKey, scope string) (blob.Record, bool, error) {
	record := blob.Record{Key: key}
	var manifest sql.NullString
	var created int64
	var expiry sql.NullInt64
	var source, facts []byte
	err := tx.QueryRowContext(ctx, `SELECT digest,manifest_digest,uploader_principal,provenance,source_identity_json,ref_facts_json,created_at,expires_at,EXISTS(SELECT 1 FROM pipeline_success_pins p WHERE p.team=b.team AND p.run_id=b.run_id) FROM blob_records b WHERE b.team=? AND b.kind=? AND b.scope=? AND b.name=?`, key.Team, key.Kind, scope, key.Name).Scan(&record.Digest, &manifest, &record.Uploader, &record.Provenance, &source, &facts, &created, &expiry, &record.Pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	if err := json.Unmarshal(source, &record.Source); err != nil {
		return record, false, err
	}
	if err := json.Unmarshal(facts, &record.RefFacts); err != nil {
		return record, false, err
	}
	record.CreatedAt = *stateTime(sql.NullInt64{Int64: created, Valid: true})
	if expiry.Valid {
		record.ExpiresAt = runspec.Optional[time.Time]{Set: true, Value: *stateTime(expiry)}
	}
	if manifest.Valid {
		record.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: blob.Digest(manifest.String)}
	}
	return record, true, nil
}

func (r *blobRecords) authorizeDigestTx(ctx context.Context, tx *storeTx, a blobActor, kind blob.Kind, digest blob.Digest) error {
	own, err := readOwnBlobReceiptTx(ctx, tx, a.source, digest)
	if err != nil {
		return err
	}
	set, err := blobReadSetTx(ctx, tx, a, digest)
	if err != nil {
		return err
	}
	if err := r.rules.AuthorizeEdge(blob.EdgeRequest{TargetKind: kind, Authority: a.authority, Digest: digest, ReadSet: set, OwnReceipt: own}); err != nil {
		return err
	}
	_, err = requireBlobTx(ctx, tx, r.tenant.team, digest)
	return err
}
