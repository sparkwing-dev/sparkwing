package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func validUploadTicket(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func blobUploadQuotaID(id string) string { return "upload:" + id }

// ReserveBlobUpload atomically reserves quota and an immutable ticket for the authorized uploader.
func (t *Tenant) ReserveBlobUpload(ctx context.Context, request UploadReservationRequest) (UploadReservation, error) {
	if !validUploadTicket(request.ID) || !validBlobDigest(request.Intent.Digest) || request.Intent.Size < 0 {
		return UploadReservation{}, authorityError(apierr.BadRequest, "invalid upload reservation")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return UploadReservation{}, err
	}
	defer rollbackOrLog(tx)
	a, err := resolveBlobAuthorityTx(ctx, tx, t.team, request.Intent.Authority, capability.DataWrite)
	if err != nil {
		return UploadReservation{}, err
	}
	if !reflect.DeepEqual(a.source, request.Intent.Identity) {
		return UploadReservation{}, authorityError(apierr.Forbidden, "upload identity differs from live authority")
	}
	if err := advisoryLockTx(ctx, tx, "upload:"+request.ID); err != nil {
		return UploadReservation{}, err
	}
	if err := advisoryLockTx(ctx, tx, "blob:"+string(t.team)+":"+string(request.Intent.Digest)); err != nil {
		return UploadReservation{}, err
	}
	if err := clearBlobTombstoneTx(ctx, tx, t.team, request.Intent.Digest); err != nil {
		return UploadReservation{}, err
	}
	prior, err := readBlobUploadTx(ctx, tx, t.team, request.ID)
	if err == nil {
		if prior.Identity != a.source || prior.Blob.Digest != request.Intent.Digest || prior.Blob.Size != request.Intent.Size || !prior.ExpiresAt.Equal(request.ExpiresAt) || prior.RefFacts.Scope != a.authority.Ref.Scope {
			return UploadReservation{}, authorityError(apierr.Conflict, "upload ticket is immutable")
		}
		if err := checkBlobUploadTx(ctx, tx, t.team, a, &prior, true); err != nil {
			return UploadReservation{}, err
		}
		return prior, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return UploadReservation{}, err
	}
	if !request.ExpiresAt.After(tx.now()) {
		return UploadReservation{}, authorityError(apierr.BadRequest, "upload expiry must be in the future")
	}
	reserved, objects, err := blobUploadReservationSizeTx(ctx, tx, t.team, a, request.Intent)
	if err != nil {
		return UploadReservation{}, err
	}
	if err := reserveBlobUploadQuotaTx(ctx, tx, t.team, request.ID, a.source.RunID, reserved, objects, request.ExpiresAt); err != nil {
		return UploadReservation{}, err
	}
	u := UploadReservation{ID: request.ID, Blob: blob.Blob{Team: string(t.team), Digest: request.Intent.Digest, Size: request.Intent.Size}, Identity: a.source, Provenance: blobActorProvenance(a), RefFacts: a.authority.Ref, ExpiresAt: request.ExpiresAt.UTC()}
	setBlobUploadKeys(&u)
	node, attempt, generation, unitKind, unitID, unitGeneration, unitSource, err := blobUploadColumns(u.Identity)
	if err != nil {
		return UploadReservation{}, err
	}
	if u.Blob.Size > 0 {
		if err := markFreeSlotActiveTx(ctx, tx, t.team); err != nil {
			return UploadReservation{}, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO uploads(id,team,run_id,store,size,provenance,expires_at,uploader_kind,unit_kind,unit_id,unit_generation,unit_source_json,digest,node_id,attempt,generation,uploader_principal,capability_prefix,repo_key,ref_scope) VALUES (?,?,?,'blob',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, u.ID, t.team, u.Identity.RunID, u.Blob.Size, u.Provenance, u.ExpiresAt.UnixNano(), u.Identity.Kind, unitKind, unitID, unitGeneration, unitSource, u.Blob.Digest, node, attempt, generation, u.Identity.Principal, u.Identity.CapabilityRef, a.authority.Repo, u.RefFacts.Scope.CapabilityScope())
	if err != nil {
		return UploadReservation{}, err
	}
	return u, tx.Commit()
}

func blobUploadReservationSizeTx(ctx context.Context, tx *storeTx, team Team, a blobActor, intent blob.UploadIntent) (bytes, objects int64, err error) {
	own, err := readOwnBlobReceiptTx(ctx, tx, a.source, intent.Digest)
	if err != nil {
		return 0, 0, err
	}
	set, err := blobReadSetTx(ctx, tx, a, intent.Digest)
	if err != nil {
		return 0, 0, err
	}
	if !own.Set && len(set.Edges) == 0 {
		return intent.Size, 1, nil
	}
	size, err := requireBlobTx(ctx, tx, team, intent.Digest)
	if errors.Is(err, blob.ErrNotFound) {
		return intent.Size, 1, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if size != intent.Size {
		return 0, 0, blob.ErrIntegrity
	}
	return 0, 0, nil
}

// LookupBlobUpload rechecks the authenticated uploader before returning its ticket.
func (t *Tenant) LookupBlobUpload(ctx context.Context, authority blob.Authority, id string) (UploadReservation, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return UploadReservation{}, err
	}
	defer rollbackOrLog(tx)
	a, err := resolveBlobAuthorityTx(ctx, tx, t.team, authority, capability.DataWrite)
	if err != nil {
		return UploadReservation{}, err
	}
	u, err := readBlobUploadTx(ctx, tx, t.team, id)
	if err != nil {
		return UploadReservation{}, err
	}
	if err := checkBlobUploadTx(ctx, tx, t.team, a, &u, true); err != nil {
		return UploadReservation{}, err
	}
	return u, tx.Commit()
}

// CommitBlobUpload atomically settles verified bytes, quota and the exact uploader receipt.
func (t *Tenant) CommitBlobUpload(ctx context.Context, request VerifiedUploadCommit) (blob.Receipt, error) {
	if !validUploadTicket(request.TicketID) || request.ReplicaID == "" || request.Verified.Team != string(t.team) || !validBlobDigest(request.Verified.Digest) || request.Verified.Size < 0 {
		return blob.Receipt{}, authorityError(apierr.BadRequest, "verified upload and replica are required")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return blob.Receipt{}, err
	}
	defer rollbackOrLog(tx)
	a, err := resolveBlobAuthorityTx(ctx, tx, t.team, request.Authority, capability.DataWrite)
	if err != nil {
		return blob.Receipt{}, err
	}
	if err := advisoryLockTx(ctx, tx, "upload:"+request.TicketID); err != nil {
		return blob.Receipt{}, err
	}
	u, err := readBlobUploadTx(ctx, tx, t.team, request.TicketID)
	if err != nil {
		return blob.Receipt{}, err
	}
	if err := checkBlobUploadTx(ctx, tx, t.team, a, &u, true); err != nil {
		return blob.Receipt{}, err
	}
	if u.Blob.Digest != request.Verified.Digest || u.Blob.Size != request.Verified.Size {
		return blob.Receipt{}, blob.ErrIntegrity
	}
	intent := blob.UploadIntent{Authority: request.Authority, Identity: u.Identity, Digest: u.Blob.Digest, Size: u.Blob.Size}
	receipt, err := commitVerifiedBlobTx(ctx, tx, t.team, a, intent, blobAccounting{ReservationID: blobUploadQuotaID(u.ID), ReplicaID: request.ReplicaID})
	if err != nil {
		return blob.Receipt{}, err
	}
	if u.CommittedAt == nil {
		result, err := tx.ExecContext(ctx, "UPDATE uploads SET committed_at=? WHERE team=? AND id=? AND committed_at=0", tx.now().UnixNano(), t.team, u.ID)
		if err != nil {
			return blob.Receipt{}, err
		}
		if err := requireOneRow(result, "upload commit"); err != nil {
			return blob.Receipt{}, err
		}
	}
	return receipt, tx.Commit()
}

// AbortBlobUpload releases an uncommitted ticket and quota under current uploader authority.
func (t *Tenant) AbortBlobUpload(ctx context.Context, authority blob.Authority, id string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	a, err := resolveBlobAuthorityTx(ctx, tx, t.team, authority, capability.DataWrite)
	if err != nil {
		return err
	}
	if err := advisoryLockTx(ctx, tx, "upload:"+id); err != nil {
		return err
	}
	u, err := readBlobUploadTx(ctx, tx, t.team, id)
	if err != nil {
		return err
	}
	if err := checkBlobUploadTx(ctx, tx, t.team, a, &u, false); err != nil {
		return err
	}
	if u.CommittedAt != nil {
		return tx.Commit()
	}
	if err := abandonBlobUploadTx(ctx, tx, t.team, id); err != nil {
		return err
	}
	return tx.Commit()
}

func setBlobUploadKeys(u *UploadReservation) {
	u.StagingKey = "teams/" + u.Blob.Team + "/pending/" + u.ID
	u.ObjectKey = "teams/" + u.Blob.Team + "/cas/" + string(u.Blob.Digest)
}

func readBlobUploadTx(ctx context.Context, tx *storeTx, team Team, id string) (UploadReservation, error) {
	u := UploadReservation{ID: id}
	var node, unitKind, unitID sql.NullString
	var attempt, generation, unitGeneration sql.NullInt64
	var source []byte
	var expires, committed int64
	var ref string
	err := tx.QueryRowContext(ctx, `SELECT run_id,size,provenance,expires_at,committed_at,uploader_kind,unit_kind,unit_id,unit_generation,unit_source_json,digest,node_id,attempt,generation,uploader_principal,capability_prefix,ref_scope FROM uploads WHERE team=? AND id=? AND store='blob'`+tx.forUpdate(), team, id).Scan(&u.Identity.RunID, &u.Blob.Size, &u.Provenance, &expires, &committed, &u.Identity.Kind, &unitKind, &unitID, &unitGeneration, &source, &u.Blob.Digest, &node, &attempt, &generation, &u.Identity.Principal, &u.Identity.CapabilityRef, &ref)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	u.Identity.Team, u.Blob.Team = string(team), string(team)
	if node.Valid {
		u.Identity.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(team), RunID: u.Identity.RunID, NodeID: node.String, Attempt: attempt.Int64, Generation: generation.Int64}}
	}
	if unitKind.Valid {
		unit := blob.UploadUnit{Kind: blob.UploadUnitKind(unitKind.String), ID: unitID.String, Generation: unitGeneration.Int64}
		if source != nil {
			unit.Source.Set = true
			if err := json.Unmarshal(source, &unit.Source.Value); err != nil {
				return u, err
			}
		}
		u.Identity.Unit = runspec.Optional[blob.UploadUnit]{Set: true, Value: unit}
	}
	u.RefFacts.Scope = blob.ParseCapabilityScope(ref)
	u.ExpiresAt = time.Unix(0, expires).UTC()
	if committed != 0 {
		at := time.Unix(0, committed).UTC()
		u.CommittedAt = &at
	}
	setBlobUploadKeys(&u)
	return u, nil
}

func checkBlobUploadTx(ctx context.Context, tx *storeTx, team Team, a blobActor, u *UploadReservation, pending bool) error {
	if u.Identity != a.source {
		return authorityError(apierr.Forbidden, "upload belongs to another uploader")
	}
	var repo string
	if err := tx.QueryRowContext(ctx, "SELECT repo_key FROM uploads WHERE team=? AND id=?", team, u.ID).Scan(&repo); err != nil {
		return err
	}
	if repo != string(a.authority.Repo) || u.RefFacts.Scope != a.authority.Ref.Scope {
		return authorityError(apierr.Conflict, "upload scope differs from sealed authority")
	}
	u.RefFacts = a.authority.Ref
	if pending && u.CommittedAt == nil {
		var state string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM quota_reservations WHERE team=? AND id=? AND quota='storage'", team, blobUploadQuotaID(u.ID)).Scan(&state); err != nil {
			return err
		}
		if state != "held" || !u.ExpiresAt.After(tx.now()) {
			return ErrLockHeld
		}
	}
	return nil
}

func blobUploadColumns(source blob.UploadIdentity) (node, attempt, generation, unitKind, unitID, unitGeneration, unitSource any, err error) {
	if source.Attempt.Set {
		a := source.Attempt.Value
		node, attempt, generation = a.NodeID, a.Attempt, a.Generation
	}
	if source.Unit.Set {
		u := source.Unit.Value
		unitKind, unitID, unitGeneration = u.Kind, u.ID, u.Generation
		if u.Source.Set {
			unitSource, err = json.Marshal(u.Source.Value)
		}
	}
	return node, attempt, generation, unitKind, unitID, unitGeneration, unitSource, err
}

func abandonBlobUploadTx(ctx context.Context, tx *storeTx, team Team, id string) error {
	if err := advisoryLockTx(ctx, tx, "upload:"+id); err != nil {
		return err
	}
	u, err := readBlobUploadTx(ctx, tx, team, id)
	if err != nil {
		return err
	}
	if u.CommittedAt != nil {
		return nil
	}
	if err := advisoryLockTx(ctx, tx, "blob:"+string(team)+":"+string(u.Blob.Digest)); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE uploads SET abandoned_at=? WHERE team=? AND id=? AND abandoned_at IS NULL", tx.now().UnixNano(), team, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 0 {
		if _, err := tx.ExecContext(ctx, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,?,?) ON CONFLICT(team,digest) DO NOTHING", team, u.Blob.Digest, u.Blob.Size, tx.now().UnixNano()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO blob_tombstones(team,digest,marked_at) VALUES (?,?,?) ON CONFLICT(team,digest) DO NOTHING", team, u.Blob.Digest, tx.now().UnixNano()); err != nil {
			return err
		}
	}
	return releaseBlobUploadQuotaTx(ctx, tx, team, id)
}

func releaseBlobUploadQuotaTx(ctx context.Context, tx *storeTx, team Team, id string) error {
	return releaseBlobUploadQuotasTx(ctx, tx, team, blobUploadQuotaID(id))
}

func releaseBlobUploadQuotasTx(ctx context.Context, tx *storeTx, team Team, id string) error {
	for _, dimension := range uploadQuotaDimensions(id, "", "", 0, 0) {
		if err := releaseBlobQuotaReservationTx(ctx, tx, team, dimension.id); err != nil {
			return err
		}
	}
	return nil
}

func releaseBlobQuotaReservationTx(ctx context.Context, tx *storeTx, team Team, reservation string) error {
	var current, period, state, quota string
	var amount int64
	if err := tx.QueryRowContext(ctx, "SELECT quota FROM quota_reservations WHERE team=? AND id=?", team, reservation).Scan(&quota); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "SELECT period FROM quota_admission WHERE team=? AND quota=?"+tx.forUpdate(), team, quota).Scan(&current); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "SELECT period,state,amount FROM quota_reservations WHERE team=? AND id=? AND quota=?"+tx.forUpdate(), team, reservation, quota).Scan(&period, &state, &amount); err != nil {
		return err
	}
	if state != "held" {
		return nil
	}
	if period == current {
		result, err := tx.ExecContext(ctx, "UPDATE quota_admission SET reserved=reserved-?,updated_at=? WHERE team=? AND quota=? AND reserved>=?", amount, tx.now().UnixNano(), team, quota, amount)
		if err != nil {
			return err
		}
		if err := requireOneRow(result, "upload abort"); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE quota_reservations SET state='released' WHERE team=? AND id=?", team, reservation)
	return err
}
