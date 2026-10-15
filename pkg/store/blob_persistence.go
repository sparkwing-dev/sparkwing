package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func commitVerifiedBlobTx(ctx context.Context, tx *storeTx, team Team, auth blobActor, intent blob.UploadIntent, accounting blobAccounting) (blob.Receipt, error) {
	quotas, err := lockBlobQuotaTx(ctx, tx, team, auth.source.RunID, accounting)
	if err != nil {
		return blob.Receipt{}, err
	}
	if err := advisoryLockTx(ctx, tx, "blob:"+string(team)+":"+string(intent.Digest)); err != nil {
		return blob.Receipt{}, err
	}
	if quotas[0].state == "committed" {
		receipt, err := readOwnBlobReceiptTx(ctx, tx, auth.source, intent.Digest)
		if err != nil {
			return blob.Receipt{}, err
		}
		if !receipt.Set {
			return blob.Receipt{}, authorityError(apierr.Conflict, "upload reservation is already committed")
		}
		size, err := requireBlobTx(ctx, tx, team, intent.Digest)
		if err != nil {
			return blob.Receipt{}, err
		}
		if size != intent.Size {
			return blob.Receipt{}, blob.ErrIntegrity
		}
		return receipt.Value, nil
	}
	inserted, err := tx.ExecContext(ctx, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,?,?) ON CONFLICT(team,digest) DO NOTHING", team, intent.Digest, intent.Size, tx.now().UnixNano())
	if err != nil {
		return blob.Receipt{}, err
	}
	objects, err := inserted.RowsAffected()
	if err != nil {
		return blob.Receipt{}, err
	}
	if quotas[0].amount < intent.Size*objects {
		return blob.Receipt{}, authorityError(apierr.Conflict, "upload exceeds its reservation")
	}
	var size int64
	if err := tx.QueryRowContext(ctx, "SELECT size FROM blobs WHERE team=? AND digest=?", team, intent.Digest).Scan(&size); err != nil {
		return blob.Receipt{}, err
	}
	if size != intent.Size {
		return blob.Receipt{}, blob.ErrIntegrity
	}
	receipt, err := insertBlobReceiptTx(ctx, tx, auth.source, intent.Digest)
	if err != nil {
		return blob.Receipt{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM blob_tombstones WHERE team=? AND digest=?", team, intent.Digest); err != nil {
		return blob.Receipt{}, err
	}
	if err := settleBlobQuotaTx(ctx, tx, team, auth.source.RunID, accounting, quotas, intent.Size*objects, objects); err != nil {
		return blob.Receipt{}, err
	}
	return receipt, nil
}

func validBlobDigest(digest blob.Digest) bool {
	decoded, err := hex.DecodeString(string(digest))
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == string(digest)
}

type blobActor struct {
	authority  blob.Authority
	source     blob.UploadIdentity
	capability capability.Capability
	pipeline   string
	mode       string
}

func resolveBlobAuthorityTx(ctx context.Context, tx *storeTx, team Team, input blob.Authority, grant capability.Grant) (blobActor, error) {
	p, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: input.CapabilityRef})
	if err != nil {
		return blobActor{}, err
	}
	c := p.Capability
	if c.Team != string(team) || c.Data == nil || !slices.Contains(c.Grants, grant) || c.Delivery == capability.DeliveryBootstrap {
		return blobActor{}, authorityError(apierr.Forbidden, "blob grant and data scope are required")
	}
	a := blobActor{capability: c}
	a.authority = blob.Authority{CapabilityRef: c.Prefix, Principal: blob.Principal(c.Subject.ID), Repo: runspec.RepoKey(c.Data.RepositoryKey)}
	if c.Data.Use == capability.DataRemoteCache {
		if grant != capability.DataRead || !c.Data.VerifiedDefaultOnly || len(c.Data.ReadRefs) == 0 {
			return a, authorityError(apierr.Forbidden, "remote cache permits verified default-branch reads only")
		}
		return a, checkBlobAuthorityHints(input, a.authority)
	}
	if c.Binding.RunID == "" || c.Data.RunID != c.Binding.RunID {
		return a, holderRefusal()
	}
	if err := lockStateRunTx(ctx, tx, team, c.Binding.RunID); err != nil {
		return a, err
	}
	var request []byte
	var repo string
	var cancelled, finished sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT request_json,repo_key,pipeline,intake_mode,cancel_requested_at,finished_at FROM runs WHERE team=? AND id=?", team, c.Binding.RunID).Scan(&request, &repo, &a.pipeline, &a.mode, &cancelled, &finished); err != nil {
		return a, err
	}
	if c.Data.Use != capability.DataPublish && (cancelled.Valid || finished.Valid) {
		return a, holderRefusal()
	}
	if repo != string(a.authority.Repo) {
		return a, authorityError(apierr.Forbidden, "data scope differs from sealed repository")
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(request, &sealed); err != nil {
		return a, err
	}
	git := sealed.Request.Git
	facts := blob.RefFacts{Scope: blob.RefScope{Ref: git.Ref}, SHA: git.SHA}
	if git.DefaultBranch.Set {
		facts.DefaultBranch = git.DefaultBranch.Value
	}
	if git.App.Set && git.App.Value.Ref == git.Ref && git.App.Value.SHA == git.SHA {
		app := git.App.Value
		facts.Scope.Verified = true
		facts.AppDeliveryKey = app.DeliveryKey
		facts.DefaultBranch = app.DefaultBranch
		facts.DefaultBranchVerified = app.DefaultBranchPush && app.Ref == app.DefaultBranch
		if sealed.Request.PullRequest.Set && sealed.Request.PullRequest.Value.BaseRepo == a.authority.Repo {
			facts.PullRequestBase = runspec.Optional[string]{Set: true, Value: sealed.Request.PullRequest.Value.BaseRef}
		}
	}
	a.authority.Ref = facts
	a.source = blob.UploadIdentity{Team: string(team), RunID: c.Binding.RunID, Principal: a.authority.Principal, CapabilityRef: c.Prefix}
	actor := Actor{Kind: ActorHolder, Authority: p.Authority, CapabilityPrefix: c.Prefix, Holder: c.Binding.Holder, Generation: c.Binding.Generation}
	switch {
	case c.Data.Use == capability.DataPublish:
		if a.mode != "publish" || c.Binding.Kind != capability.BindingRun {
			return a, holderRefusal()
		}
		a.source.Kind = blob.UploadPublication
		if input.Attempt.Set {
			attempt := input.Attempt.Value
			var exists int
			err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM node_attempts WHERE team=? AND run_id=? AND node_id=? AND attempt=? AND generation=? AND finished_at IS NOT NULL", team, c.Binding.RunID, attempt.NodeID, attempt.Attempt, attempt.Generation).Scan(&exists)
			if err != nil {
				return a, err
			}
			if exists != 1 || attempt.Team != string(team) || attempt.RunID != c.Binding.RunID {
				return a, holderRefusal()
			}
			a.source.Attempt = input.Attempt
			a.authority.Attempt = input.Attempt
		}
	case c.Unit == capability.UnitNode:
		row, err := readNodeTx(ctx, tx, team, NodeRef{c.Binding.RunID, c.Binding.NodeID})
		if err != nil {
			return a, err
		}
		if row.CancelRequestedAt != nil {
			return a, holderRefusal()
		}
		if err := checkHolderTx(ctx, tx, team, row, actor, tx.now()); err != nil {
			return a, err
		}
		if row.Attempt <= 0 {
			return a, holderRefusal()
		}
		var prefix string
		err = tx.QueryRowContext(ctx, "SELECT capability_prefix FROM node_attempts WHERE team=? AND run_id=? AND node_id=? AND attempt=? AND generation=? AND finished_at IS NULL", team, c.Binding.RunID, c.Binding.NodeID, row.Attempt, row.Generation).Scan(&prefix)
		if err != nil {
			return a, err
		}
		if prefix != c.Prefix {
			return a, holderRefusal()
		}
		a.source.Kind = blob.UploadJob
		a.source.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(team), RunID: c.Binding.RunID, NodeID: c.Binding.NodeID, Attempt: row.Attempt, Generation: row.Generation}}
		a.authority.Attempt = a.source.Attempt
	case c.Unit == capability.UnitTrigger || c.Unit == capability.UnitPlan || c.Unit == capability.UnitEval:
		unit := blob.LogUnit{Kind: blob.LogUnitKind(c.Unit), Generation: c.Binding.Generation}
		upload := blob.UploadUnit{Kind: blob.UploadUnitKind(c.Unit), ID: c.UnitID, Generation: c.Binding.Generation}
		if c.Unit == capability.UnitEval {
			if c.Binding.Expansion == nil {
				return a, holderRefusal()
			}
			unit.Generation = c.Binding.Expansion.Generation
			unit.Source = &blob.LogSource{NodeID: c.Binding.NodeID, Generation: c.Binding.Generation}
			upload.Generation = unit.Generation
			var ordinal int64
			if err := tx.QueryRowContext(ctx, "SELECT attempt FROM nodes WHERE team=? AND run_id=? AND node_id=? AND generation=?", team, c.Binding.RunID, c.Binding.NodeID, c.Binding.Generation).Scan(&ordinal); err != nil {
				return a, err
			}
			upload.Source = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(team), RunID: c.Binding.RunID, NodeID: c.Binding.NodeID, Attempt: ordinal, Generation: c.Binding.Generation}}
			actor.Generation = unit.Generation
		}
		subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: string(team), RunID: c.Binding.RunID, Unit: &unit}
		if err := checkOwnerLogWriterTx(ctx, tx, team, subject, c, actor); err != nil {
			return a, err
		}
		a.source.Kind = blob.UploadOwnerUnit
		a.source.Unit = runspec.Optional[blob.UploadUnit]{Set: true, Value: upload}
	default:
		return a, holderRefusal()
	}
	a.authority.Upload = runspec.Optional[blob.UploadIdentity]{Set: true, Value: a.source}
	return a, checkBlobAuthorityHints(input, a.authority)
}

func checkBlobAuthorityHints(input, actual blob.Authority) error {
	if (input.Principal != "" && input.Principal != actual.Principal) || (input.Repo != "" && input.Repo != actual.Repo) || (input.Attempt.Set && input.Attempt != actual.Attempt) || (input.Ref != (blob.RefFacts{}) && input.Ref != actual.Ref) || (input.Upload.Set && input.Upload != actual.Upload) {
		return authorityError(apierr.Forbidden, "blob authority differs from persisted scope")
	}
	return nil
}

func blobActorProvenance(a blobActor) blob.Provenance {
	if a.mode != "publish" && a.capability.Billing != nil && a.capability.Billing.ExecutorClass == "cloud" {
		return blob.ProvenanceCloud
	}
	return blob.ProvenanceLocal
}

func insertBlobReceiptTx(ctx context.Context, tx *storeTx, source blob.UploadIdentity, digest blob.Digest) (blob.Receipt, error) {
	body, err := json.Marshal(source)
	if err != nil {
		return blob.Receipt{}, err
	}
	sum := sha256.Sum256(append(body, []byte(digest)...))
	id := hex.EncodeToString(sum[:])
	node, attempt, generation, unitKind, unitID, unitGeneration, unitSource, err := blobUploadColumns(source)
	if err != nil {
		return blob.Receipt{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO blob_receipts(team,id,run_id,node_id,attempt,generation,uploader_kind,unit_kind,unit_id,unit_generation,unit_source_json,digest,uploader_principal,capability_prefix,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(team,id) DO NOTHING`, source.Team, id, source.RunID, node, attempt, generation, source.Kind, unitKind, unitID, unitGeneration, unitSource, digest, source.Principal, source.CapabilityRef, tx.now().UnixNano())
	if err != nil {
		return blob.Receipt{}, err
	}
	var created int64
	if err := tx.QueryRowContext(ctx, "SELECT created_at FROM blob_receipts WHERE team=? AND id=?", source.Team, id).Scan(&created); err != nil {
		return blob.Receipt{}, err
	}
	return blob.Receipt{Identity: source, Digest: digest, CreatedAt: *stateTime(sql.NullInt64{Int64: created, Valid: true})}, nil
}

func readOwnBlobReceiptTx(ctx context.Context, tx *storeTx, source blob.UploadIdentity, digest blob.Digest) (runspec.Optional[blob.Receipt], error) {
	body, err := json.Marshal(source)
	if err != nil {
		return runspec.Optional[blob.Receipt]{}, err
	}
	sum := sha256.Sum256(append(body, []byte(digest)...))
	var at int64
	err = tx.QueryRowContext(ctx, "SELECT created_at FROM blob_receipts WHERE team=? AND id=? AND digest=?", source.Team, hex.EncodeToString(sum[:]), digest).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return runspec.Optional[blob.Receipt]{}, nil
	}
	if err != nil {
		return runspec.Optional[blob.Receipt]{}, err
	}
	return runspec.Optional[blob.Receipt]{Set: true, Value: blob.Receipt{Identity: source, Digest: digest, CreatedAt: *stateTime(sql.NullInt64{Int64: at, Valid: true})}}, nil
}

func blobKindAllowed(source blob.UploadIdentity, kind blob.Kind) bool {
	for _, rule := range blob.UploadRules {
		if rule.IdentityKind == source.Kind && rule.RequiresAttempt == source.Attempt.Set && rule.RequiresUnit == source.Unit.Set && slices.Contains(rule.RecordKinds, kind) {
			return true
		}
	}
	return false
}

func requireBlobTx(ctx context.Context, tx *storeTx, team Team, digest blob.Digest) (int64, error) {
	if !validBlobDigest(digest) {
		return 0, authorityError(apierr.BadRequest, "invalid blob digest")
	}
	var size int64
	err := tx.QueryRowContext(ctx, "SELECT size FROM blobs WHERE team=? AND digest=?", team, digest).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, blob.ErrNotFound
	}
	return size, err
}

func blobIsCache(kind blob.Kind) bool         { return kind == blob.KindMemo || kind == blob.KindDepcache }
func blobLocalRepo(repo runspec.RepoKey) bool { return strings.HasPrefix(string(repo), "local:") }
