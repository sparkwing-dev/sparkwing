package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func validDataDigest(digest blob.Digest) bool {
	decoded, err := hex.DecodeString(string(digest))
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == string(digest)
}

func (s *boundData) Upload(ctx context.Context, intent blob.UploadIntent) (blob.UploadTicket, error) {
	authority, err := s.live(ctx, intent.Authority)
	if err != nil {
		return blob.UploadTicket{}, err
	}
	if !authority.Upload.Set || intent.Identity != authority.Upload.Value {
		return blob.UploadTicket{}, dataForbidden()
	}
	if !validDataDigest(intent.Digest) || intent.Size < 0 {
		return blob.UploadTicket{}, invalidEngineBody("upload requires a SHA-256 digest and nonnegative size")
	}
	if err := s.authorize(ctx, capability.DataUpload, authority, capability.DataTarget{Digest: string(intent.Digest)}); err != nil {
		return blob.UploadTicket{}, err
	}
	tenant, err := s.controller.dataTransactions(ctx, s.authorized.Target.Team)
	if err != nil {
		return blob.UploadTicket{}, err
	}
	if s.controller.Objects == nil {
		return blob.UploadTicket{}, missingEndpointPolicy()
	}
	id := rand.Text()
	expires := time.Now().UTC().Add(limits.DataUploadURLTTL)
	reservation, err := tenant.ReserveBlobUpload(ctx, store.UploadReservationRequest{ID: id, Intent: intent, ExpiresAt: expires})
	if err != nil {
		return blob.UploadTicket{}, engineLookupError(err)
	}
	if err := validateDataReservation(reservation, authority, id); err != nil {
		return blob.UploadTicket{}, err
	}
	if reservation.Blob.Digest != intent.Digest || reservation.Blob.Size != intent.Size || reservation.ExpiresAt.After(expires) || !time.Now().Before(reservation.ExpiresAt) {
		return blob.UploadTicket{}, dataProjectionError()
	}
	object := capability.ObjectToSign{Method: http.MethodPut, Key: reservation.StagingKey, Size: reservation.Blob.Size, SHA256: string(reservation.Blob.Digest), ExpiresAt: reservation.ExpiresAt}
	signed, err := s.controller.Objects.Sign(ctx, object)
	if err == nil && !signedDataMatches(signed, object) {
		err = dataProjectionError()
	}
	if err != nil {
		// safety: settlement must release quota even when the signing request was cancelled.
		cleanup := context.WithoutCancel(ctx)
		abortErr := tenant.AbortBlobUpload(cleanup, authority, reservation.ID)
		if abortErr != nil {
			return blob.UploadTicket{}, errors.Join(err, abortErr)
		}
		return blob.UploadTicket{}, errors.Join(err, s.controller.Objects.DeleteObject(cleanup, reservation.StagingKey))
	}
	return blob.UploadTicket{ID: reservation.ID, Transfer: dataTransfer(signed)}, nil
}

func validateDataReservation(r store.UploadReservation, authority blob.Authority, id string) error {
	team := r.Identity.Team
	if !authority.Upload.Set || r.ID == "" || r.ID != id || strings.ContainsAny(r.ID, "/\\\x00") ||
		team == "" || strings.ContainsAny(team, "/\\\x00") || r.Identity != authority.Upload.Value ||
		r.Blob.Team != team || !validDataDigest(r.Blob.Digest) || r.Blob.Size < 0 || r.ExpiresAt.IsZero() ||
		r.StagingKey != "teams/"+team+"/pending/"+r.ID || r.ObjectKey != "teams/"+team+"/cas/"+string(r.Blob.Digest) {
		return dataProjectionError()
	}
	return nil
}

func signedDataMatches(signed capability.SignedObject, object capability.ObjectToSign) bool {
	return signed.URL != "" && signed.SHA256 == object.SHA256 && signed.Size == object.Size && signed.ExpiresAt.Equal(object.ExpiresAt)
}

func dataTransfer(signed capability.SignedObject) blob.SignedTransfer {
	return blob.SignedTransfer{URL: signed.URL, Headers: signed.Headers, ExpiresAt: signed.ExpiresAt, Digest: blob.Digest(signed.SHA256), Size: signed.Size}
}

func (s *boundData) Commit(ctx context.Context, request blob.CommitUpload) (blob.Receipt, error) {
	authority, err := s.live(ctx, request.Authority)
	if err != nil {
		return blob.Receipt{}, err
	}
	if !authority.Upload.Set || request.TicketID == "" {
		return blob.Receipt{}, dataForbidden()
	}
	tenant, err := s.controller.dataTransactions(ctx, s.authorized.Target.Team)
	if err != nil {
		return blob.Receipt{}, err
	}
	if s.controller.Objects == nil {
		return blob.Receipt{}, missingEndpointPolicy()
	}
	reservation, err := tenant.LookupBlobUpload(ctx, authority, request.TicketID)
	if err != nil {
		return blob.Receipt{}, engineLookupError(err)
	}
	if err := validateDataReservation(reservation, authority, request.TicketID); err != nil {
		return blob.Receipt{}, err
	}
	if err := s.authorize(ctx, capability.DataCommit, authority, capability.DataTarget{UploadID: request.TicketID, Digest: string(reservation.Blob.Digest)}); err != nil {
		return blob.Receipt{}, err
	}
	if reservation.CommittedAt == nil {
		if err := s.controller.Objects.CommitObject(ctx, reservation.StagingKey, reservation.Blob); err != nil {
			return blob.Receipt{}, err
		}
	}
	receipt, err := tenant.CommitBlobUpload(ctx, store.VerifiedUploadCommit{Authority: authority, TicketID: request.TicketID, Verified: reservation.Blob, ReplicaID: s.controller.replicaID})
	if err != nil {
		return blob.Receipt{}, engineLookupError(err)
	}
	if receipt.Identity != reservation.Identity || receipt.Digest != reservation.Blob.Digest {
		return blob.Receipt{}, dataProjectionError()
	}
	if err := s.controller.Objects.DeleteObject(context.WithoutCancel(ctx), reservation.StagingKey); err != nil {
		return blob.Receipt{}, err
	}
	return receipt, nil
}
