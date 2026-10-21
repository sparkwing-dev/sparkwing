package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const sourceBundleRetention = 24 * time.Hour

// SourceUploadScope binds a submitter's bytes to both selected source revisions.
type SourceUploadScope struct {
	Repository                runspec.RepoKey
	RepositoryDisplay         string
	ExecutionSHA, PipelineSHA string
}

// SourceUpload retains a pre-intake ticket and its verified object evidence.
type SourceUpload struct {
	ID          string
	Scope       SourceUploadScope
	Blob        blob.Blob
	ExpiresAt   time.Time
	CommittedAt int64
}

func sourceUploaderTx(ctx context.Context, tx *storeTx, team Team, authority capability.AuthorityRef, scope SourceUploadScope) (capability.Principal, error) {
	p, err := tx.ResolveAuthority(ctx, authority)
	if err != nil {
		return p, err
	}
	c := p.Capability
	if c.Team != string(team) || c.Audience != capability.Controller || c.Binding.Kind != capability.BindingNone || !slices.Contains(c.Grants, capability.RunsWrite) || !capability.RepositoryAllowed(c.Repositories, capability.RepositoryIdentity{Key: string(scope.Repository), Display: scope.RepositoryDisplay}) {
		return p, authorityError(apierr.Forbidden, "source upload requires repository-bound run submission authority")
	}
	return p, nil
}

// ReserveSourceUpload reserves monthly storage before signing any source transfer.
func (t *Tenant) ReserveSourceUpload(ctx context.Context, authority capability.AuthorityRef, u SourceUpload) (SourceUpload, error) {
	if !validUploadTicket(u.ID) || !validBlobDigest(u.Blob.Digest) || u.Blob.Size < 0 || u.Scope.Repository == "" || !runspec.IsCommitID(u.Scope.ExecutionSHA) || !runspec.IsCommitID(u.Scope.PipelineSHA) {
		return u, authorityError(apierr.BadRequest, "source upload requires a repository, full revisions, digest and size")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return u, err
	}
	defer rollbackOrLog(tx)
	if _, err := sourceUploaderTx(ctx, tx, t.team, authority, u.Scope); err != nil {
		return u, err
	}
	if !u.ExpiresAt.After(tx.time) {
		return u, ErrInvalidInput
	}
	if err := advisoryLockTx(ctx, tx, "blob:"+string(t.team)+":"+string(u.Blob.Digest)); err != nil {
		return u, err
	}
	if err := clearBlobTombstoneTx(ctx, tx, t.team, u.Blob.Digest); err != nil {
		return u, err
	}
	policy, err := storageQuotaForTeamTx(ctx, tx, t.team)
	if err != nil {
		return u, err
	}
	reserved := u.Blob.Size
	var own int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM source_uploads WHERE team=? AND authority_kind=? AND authority_id=? AND repo_key=? AND digest=? AND size=? AND committed_at>0 AND expires_at>?`, t.team, authority.Kind, authority.ID, u.Scope.Repository, u.Blob.Digest, u.Blob.Size, tx.time.UnixNano()).Scan(&own); err != nil {
		return u, err
	}
	if own > 0 {
		reserved = 0
	}
	d := uploadQuotaDimension{id: blobUploadQuotaID(u.ID), quota: "storage", period: tx.time.Format("2006-01"), amount: reserved, ceiling: policy.MaxBytesPerMonth, limitName: StorageLimitBytesPerMonth, unit: "bytes"}
	if err := reserveUploadQuotaDimensionTx(ctx, tx, t.team, d, u.ExpiresAt); err != nil {
		return u, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO source_uploads(team,id,authority_kind,authority_id,repo_key,repo_display,execution_sha,pipeline_sha,digest,size,expires_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, t.team, u.ID, authority.Kind, authority.ID, u.Scope.Repository, u.Scope.RepositoryDisplay, u.Scope.ExecutionSHA, u.Scope.PipelineSHA, u.Blob.Digest, u.Blob.Size, u.ExpiresAt.UnixNano())
	if err != nil {
		return u, err
	}
	u.Blob.Team = string(t.team)
	return u, tx.Commit()
}

func readSourceUploadTx(ctx context.Context, tx *storeTx, team Team, authority capability.AuthorityRef, id string) (SourceUpload, error) {
	u := SourceUpload{ID: id, Blob: blob.Blob{Team: string(team)}}
	var expires int64
	err := tx.QueryRowContext(ctx, `SELECT repo_key,repo_display,execution_sha,pipeline_sha,digest,size,expires_at,committed_at FROM source_uploads WHERE team=? AND id=? AND authority_kind=? AND authority_id=?`+tx.forUpdate(), team, id, authority.Kind, authority.ID).Scan(&u.Scope.Repository, &u.Scope.RepositoryDisplay, &u.Scope.ExecutionSHA, &u.Scope.PipelineSHA, &u.Blob.Digest, &u.Blob.Size, &expires, &u.CommittedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, blob.ErrDigestNotReadable
	}
	if err != nil {
		return u, err
	}
	u.ExpiresAt = time.Unix(0, expires).UTC()
	if _, err := sourceUploaderTx(ctx, tx, team, authority, u.Scope); err != nil {
		return u, err
	}
	if !u.ExpiresAt.After(tx.time) {
		return u, authorityError(apierr.Conflict, "source upload expired; upload again")
	}
	return u, nil
}

// ReadSourceUpload rechecks the original submitter and current repository authority.
func (t *Tenant) ReadSourceUpload(ctx context.Context, authority capability.AuthorityRef, id string) (SourceUpload, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return SourceUpload{}, err
	}
	defer rollbackOrLog(tx)
	u, err := readSourceUploadTx(ctx, tx, t.team, authority, id)
	if err != nil {
		return u, err
	}
	return u, tx.Commit()
}

// CommitSourceUpload settles only bytes verified by the shared object backend.
func (t *Tenant) CommitSourceUpload(ctx context.Context, authority capability.AuthorityRef, id string, verified blob.Blob, replica string) (SourceUpload, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return SourceUpload{}, err
	}
	defer rollbackOrLog(tx)
	u, err := readSourceUploadTx(ctx, tx, t.team, authority, id)
	if err != nil {
		return u, err
	}
	if verified.Team != string(t.team) || verified.Digest != u.Blob.Digest || verified.Size != u.Blob.Size || replica == "" {
		return u, blob.ErrIntegrity
	}
	if u.CommittedAt != 0 {
		return u, tx.Commit()
	}
	if err := advisoryLockTx(ctx, tx, "blob:"+string(t.team)+":"+string(u.Blob.Digest)); err != nil {
		return u, err
	}
	q, err := lockUploadQuotaDimensionTx(ctx, tx, t.team, blobUploadQuotaID(id), "storage")
	if err != nil {
		return u, err
	}
	n, err := commitBlobBytesTx(ctx, tx, t.team, u.Blob, q.amount)
	if err != nil {
		return u, err
	}
	d := uploadQuotaDimension{id: blobUploadQuotaID(id), quota: "storage", amount: u.Blob.Size * n}
	if err := settleUploadQuotaDimensionTx(ctx, tx, t.team, d, q); err != nil {
		return u, err
	}
	if err := recordStorageUsageTx(ctx, tx, t.team, "", replica, q.period, u.Blob.Size*n, n); err != nil {
		return u, err
	}
	u.CommittedAt = tx.time.UnixNano()
	u.ExpiresAt = tx.time.Add(sourceBundleRetention)
	_, err = tx.ExecContext(ctx, "UPDATE source_uploads SET committed_at=?,expires_at=?,replica_id=?,period=?,charged_objects=? WHERE team=? AND id=?", u.CommittedAt, u.ExpiresAt.UnixNano(), replica, q.period, n, t.team, id)
	if err != nil {
		return u, err
	}
	return u, tx.Commit()
}

func bindSourceUploadTx(ctx context.Context, tx *storeTx, team Team, request IntakeRequest, result AdmissionResult, principal capability.Principal) error {
	input := result.Request.Request
	if !input.Source.BundleDigest.Set {
		return nil
	}
	if request.Request.Lineage.RetryOf != "" {
		return copyRetrySourceRecordTx(ctx, tx, team, request.Request.Lineage.RetryOf, result.RunID)
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM source_uploads WHERE team=? AND authority_kind=? AND authority_id=? AND repo_key=? AND execution_sha=? AND pipeline_sha=? AND digest=? AND committed_at>0 AND expires_at>? AND bound_run_id IS NULL ORDER BY id LIMIT 1`+tx.forUpdate(), team, request.Authority.Kind, request.Authority.ID, input.Git.Repository.Key, input.Git.SHA, input.Source.Ref, input.Source.BundleDigest.Value, tx.time.UnixNano()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return authorityError(apierr.Forbidden, "source bundle is not committed by this submitter for these revisions")
	}
	if err != nil {
		return err
	}
	u, err := readSourceUploadTx(ctx, tx, team, request.Authority, id)
	if err != nil {
		return err
	}
	policy, err := storageQuotaForTeamTx(ctx, tx, team)
	if err != nil {
		return err
	}
	var period, replica string
	var objects int64
	if err := tx.QueryRowContext(ctx, "SELECT period,replica_id,charged_objects FROM source_uploads WHERE team=? AND id=?", team, id).Scan(&period, &replica, &objects); err != nil {
		return err
	}
	dimensions := uploadQuotaDimensions("", result.RunID, "", u.Blob.Size*objects, objects)
	dimensions[1].ceiling = policy.MaxBytesPerRun
	dimensions[2].ceiling = policy.MaxObjectsPerRun
	for _, d := range dimensions[1:] {
		if _, err := applyStorageQuotaDimensionTx(ctx, tx, team, d, false); err != nil {
			return err
		}
	}
	if err := recordStorageRunUsageTx(ctx, tx, team, result.RunID, replica, period, u.Blob.Size*objects, objects); err != nil {
		return err
	}
	identity := blob.UploadIdentity{Kind: blob.UploadSubmission, Team: string(team), RunID: result.RunID, Principal: blob.Principal(principal.Capability.Subject.ID), CapabilityRef: request.Authority.ID}
	source, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	scope := blob.ScopeFor(result.Request.Provenance)
	facts, err := json.Marshal(blob.RefFacts{Scope: scope, SHA: input.Git.SHA})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO blob_records(team,kind,scope,name,digest,run_id,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,source_sha,ref_facts_json,pipeline,created_at) VALUES (?,'source_bundle',?,'source',?,?,?, ?,?,'local',?,'submission',?,?,0,?,?,?,?)`, team, result.RunID, u.Blob.Digest, result.RunID, input.Git.Repository.Key, scope.CapabilityScope(), identity.Principal, source, result.RunID, boolInt(scope.Verified), input.Git.SHA, facts, input.Pipeline, tx.time.UnixNano())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE source_uploads SET bound_run_id=? WHERE team=? AND id=? AND bound_run_id IS NULL", result.RunID, team, id)
	return err
}

func copyRetrySourceRecordTx(ctx context.Context, tx *storeTx, team Team, previous, run string) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO blob_records(team,kind,scope,name,digest,run_id,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,source_sha,ref_facts_json,pipeline,created_at) SELECT team,kind,?,name,digest,?,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,source_sha,ref_facts_json,pipeline,? FROM blob_records WHERE team=? AND run_id=? AND kind='source_bundle' AND source_kind='submission'`, run, run, tx.time.UnixNano(), team, previous)
	if err != nil {
		return err
	}
	return requireOneRow(result, "retry source binding")
}

// ExpireSourceUploads removes staged source bytes under the maintenance lease before releasing tickets.
func (o *Operator) ExpireSourceUploads(ctx context.Context, lease SingletonLease, limit int, objects blob.ObjectBackend) error {
	if limit <= 0 || objects == nil {
		return ErrInvalidInput
	}
	var expired []teamKey
	err := o.s.inTx(ctx, func(tx *storeTx) (err error) {
		if err := assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.time); err != nil {
			return err
		}
		expired, err = teamKeysTx(ctx, tx, "SELECT team,id FROM source_uploads WHERE expires_at<=? ORDER BY team,expires_at,id LIMIT ?", tx.time.UnixNano(), limit)
		return err
	})
	for _, e := range expired {
		if err != nil {
			return err
		}
		if err = objects.DeleteObject(ctx, "teams/"+e.team+"/pending/"+e.key); err == nil {
			err = o.s.inTx(ctx, func(tx *storeTx) error {
				if err := assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.time); err != nil {
					return err
				}
				return expireSourceUploadTx(ctx, tx, Team(e.team), e.key)
			})
		}
	}
	return err
}

func expireSourceUploadTx(ctx context.Context, tx *storeTx, team Team, id string) error {
	var committed int64
	err := tx.QueryRowContext(ctx, "SELECT committed_at FROM source_uploads WHERE team=? AND id=? AND expires_at<=?"+tx.forUpdate(), team, id, tx.time.UnixNano()).Scan(&committed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if committed == 0 {
		if err := releaseBlobQuotaReservationTx(ctx, tx, team, blobUploadQuotaID(id)); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM source_uploads WHERE team=? AND id=?", team, id)
	return err
}

// AbortSourceUpload rolls back a failed signing operation under current submission authority.
func (t *Tenant) AbortSourceUpload(ctx context.Context, authority capability.AuthorityRef, id string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	u, err := readSourceUploadTx(ctx, tx, t.team, authority, id)
	if err != nil {
		return err
	}
	if u.CommittedAt != 0 {
		return authorityError(apierr.Conflict, "source upload is committed")
	}
	if err := releaseBlobQuotaReservationTx(ctx, tx, t.team, blobUploadQuotaID(id)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM source_uploads WHERE team=? AND id=? AND committed_at=0", t.team, id); err != nil {
		return err
	}
	return tx.Commit()
}
