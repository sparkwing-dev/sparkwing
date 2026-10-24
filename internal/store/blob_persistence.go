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
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func commitVerifiedBlobTx(ctx context.Context, tx *storeTx, team Team, auth blobActor, intent blob.UploadIntent, accounting blobAccounting) (blob.Receipt, error) {
	if err := advisoryLockTx(ctx, tx, "blob:"+string(team)+":"+string(intent.Digest)); err != nil {
		return blob.Receipt{}, err
	}
	quotas, err := lockBlobQuotaTx(ctx, tx, team, auth.source.RunID, accounting)
	if err != nil {
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
	objects, err := commitBlobBytesTx(ctx, tx, team, blob.Blob{Digest: intent.Digest, Size: intent.Size}, quotas[0].amount)
	if err != nil {
		return blob.Receipt{}, err
	}
	receipt, err := insertBlobReceiptTx(ctx, tx, auth.source, intent.Digest)
	if err != nil {
		return blob.Receipt{}, err
	}
	if err := clearBlobTombstoneTx(ctx, tx, team, intent.Digest); err != nil {
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
	if c.Team != string(team) || c.Data == nil || !slices.Contains(c.Grants, grant) ||
		c.Delivery == capability.DeliveryBootstrap && grant != capability.SourceRead || grant == capability.SourceRead && c.Delivery != capability.DeliveryBootstrap {
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
	facts, err := provenanceRefFacts(sealed)
	if err != nil {
		return a, err
	}
	a.authority.Ref = facts
	a.source = blob.UploadIdentity{Team: string(team), RunID: c.Binding.RunID, Principal: a.authority.Principal, CapabilityRef: c.Prefix}
	if grant == capability.SourceRead {
		if c.Data.Use != capability.DataClaim && !(c.Data.Use == capability.DataExpansion && c.Unit == capability.UnitEval) || !sealed.Request.Source.BundleDigest.Set || sealed.Request.Source.BundleDigest.Value == "" {
			return a, bindingRefusal()
		}
		if err := checkSourceReleaseFenceTx(ctx, tx, c); err != nil {
			return a, err
		}
		return a, checkBlobAuthorityHints(input, a.authority)
	}
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: p.Authority, CapabilityPrefix: c.Prefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(c.Binding.RunID), NodeID: runspec.NodeID(c.Binding.NodeID), Holder: c.Binding.Holder, Generation: runspec.ClaimGeneration(c.Binding.Generation)}}
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
		row, err := readNodeTx(ctx, tx, team, runspec.NodeRef{RunID: runspec.RunID(c.Binding.RunID), NodeID: runspec.NodeID(c.Binding.NodeID)})
		if err != nil {
			return a, err
		}
		if row.CancelRequestedAt != nil {
			return a, holderRefusal()
		}
		if err := checkHolderTx(ctx, tx, team, row, actor); err != nil {
			return a, err
		}
		// safety: source reads precede execution; output writes require an attempt.
		if row.Status == runstate.StatusPending && row.StartedAt == nil && grant == capability.DataRead {
			return a, checkBlobAuthorityHints(input, a.authority)
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
		a.source.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(team), RunID: c.Binding.RunID, NodeID: c.Binding.NodeID, Attempt: int64(row.Attempt), Generation: int64(row.Generation)}}
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
			actor.Generation = runspec.ClaimGeneration(unit.Generation)
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
	if a.mode != "publish" && a.capability.Billing != nil && a.capability.Billing.ExecutorClass == TierCloud {
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
	now := tx.now(ctx)
	_, err = tx.execContext(ctx, `INSERT INTO blob_receipts(team,id,run_id,node_id,attempt,generation,uploader_kind,unit_kind,unit_id,unit_generation,unit_source_json,digest,uploader_principal,capability_prefix,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(team,id) DO NOTHING`, source.Team, id, source.RunID, node, attempt, generation, source.Kind, unitKind, unitID, unitGeneration, unitSource, digest, source.Principal, source.CapabilityRef, now.UnixNano())
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

func commitBlobBytesTx(ctx context.Context, tx *storeTx, team Team, object blob.Blob, reserved int64) (int64, error) {
	if object.Size > 0 {
		if err := markFreeSlotActiveTx(ctx, tx, team); err != nil {
			return 0, err
		}
	}
	now := tx.now(ctx)
	inserted, err := tx.execContext(ctx, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,?,?) ON CONFLICT(team,digest) DO NOTHING", team, object.Digest, object.Size, now.UnixNano())
	if err != nil {
		return 0, err
	}
	objects, err := inserted.RowsAffected()
	if err != nil {
		return 0, err
	}
	if reserved < object.Size*objects {
		return 0, authorityError(apierr.Conflict, "upload exceeds its reservation")
	}
	var size int64
	if err := tx.QueryRowContext(ctx, "SELECT size FROM blobs WHERE team=? AND digest=?", team, object.Digest).Scan(&size); err != nil {
		return 0, err
	}
	if size != object.Size {
		return 0, blob.ErrIntegrity
	}
	if err := clearBlobTombstoneTx(ctx, tx, team, object.Digest); err != nil {
		return 0, err
	}
	return objects, nil
}

func blobCacheRepositoryAllowed(a blobActor, kind blob.Kind) bool {
	return !blobLocalRepo(a.authority.Repo) || kind == blob.KindDepcache && a.capability.Subject.Kind == capability.SubjectLocal && a.capability.Data.Use == capability.DataClaim
}

// provenanceRefFacts is the one owner of a run's data scope: the scope it writes,
// from its trust class, and the verified fallbacks it reads, from the default
// branch and pull request base its provenance sealed.
func provenanceRefFacts(sealed runspec.SealedRequest) (blob.RefFacts, error) {
	p := sealed.Provenance
	if !p.Valid() {
		return blob.RefFacts{}, authorityError(apierr.IntegrityFailure, "run has no sealed provenance")
	}
	facts := blob.RefFacts{Scope: blob.ScopeFor(p), SHA: p.SHA()}
	if p.DefaultBranch() != "" {
		facts.DefaultBranch = "refs/heads/" + p.DefaultBranch()
	}
	if !p.Verified() {
		return facts, nil
	}
	facts.AppDeliveryKey = p.Delivery()
	facts.DefaultBranchVerified = p.Trust() == runspec.TrustVerifiedDefault
	if base := p.PullRequestBase(); base != "" {
		facts.PullRequestBase = runspec.Optional[string]{Set: true, Value: "refs/heads/" + base}
	}
	return facts, nil
}

// readScopes lists what a run may read: its own scope, then the verified default
// branch and a pull request's base. Readers still require verified provenance on
// any record outside the run's own scope.
func readScopes(facts blob.RefFacts) []string {
	scopes := []string{facts.Scope.CapabilityScope()}
	for _, ref := range []string{facts.DefaultBranch, facts.PullRequestBase.Value} {
		if ref != "" && !slices.Contains(scopes, ref) {
			scopes = append(scopes, ref)
		}
	}
	return scopes
}
