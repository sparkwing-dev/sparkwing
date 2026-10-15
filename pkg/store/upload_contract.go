package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// UploadReservationRequest is trusted adapter input, never an HTTP body.
// ID is an opaque ticket derived within the route's tenant/principal idempotency
// scope; retries reuse it and must present the same immutable upload facts.
// It is one ASCII letter/digit/underscore/hyphen path segment.
// The store derives provenance and ref facts from live authority and sealed state.
type UploadReservationRequest struct {
	ID        string
	Intent    blob.UploadIntent
	ExpiresAt time.Time
}

// UploadReservation is the immutable uploads row plus derived object keys.
// StagingKey is teams/<team>/pending/<id>; ObjectKey is the immutable CAS key.
// Neither key, provenance nor uploader identity is accepted from caller JSON.
type UploadReservation struct {
	ID          string
	Blob        blob.Blob
	Identity    blob.UploadIdentity
	Provenance  blob.Provenance
	RefFacts    blob.RefFacts
	StagingKey  string
	ObjectKey   string
	ExpiresAt   time.Time
	CommittedAt *time.Time
}

// VerifiedUploadCommit follows successful ObjectBackend.CommitObject.
// Verified is server-observed size/digest evidence, not a client's assertion.
// Identity, provenance, ref facts and quota accounting come from the ticket.
// ReplicaID comes from controller configuration, never the commit body.
type VerifiedUploadCommit struct {
	Authority blob.Authority
	TicketID  string
	Verified  blob.Blob
	ReplicaID string
}

// ReserveBlobUpload binds the exact uploader and quota reservation in one transaction.
// It reserves monthly bytes and lifetime run bytes/objects. Authorized existing
// content reserves zero; a new empty blob still reserves one object.
// It rechecks tenant, live capability, owner/job generation and publication rules.
// A ticket replay compares every immutable fact; an existing CAS object does not
// replace the upload or manufacture a receipt for a caller lacking a read edge.
// ExpiresAt is server-selected under the existing signing policy.
func (t *Tenant) ReserveBlobUpload(ctx context.Context, request UploadReservationRequest) (UploadReservation, error) {
	return t.reserveBlobUpload(ctx, request)
}

// LookupBlobUpload resolves only the authenticated uploader's ticket in this tenant.
// The adapter uses it to reauthorize immediately before physical publication.
// Expired uncommitted tickets cannot proceed; committed tickets remain available
// for exact replay while the caller retains the required authority.
func (t *Tenant) LookupBlobUpload(ctx context.Context, authority blob.Authority, ticketID string) (UploadReservation, error) {
	return t.lookupBlobUpload(ctx, authority, ticketID)
}

// CommitBlobUpload rechecks live authority and all ticket fences transactionally.
// Verified must match the ticket's tenant, digest and size. It records blob
// existence, the exact uploader receipt and committed_at, consumes the reservation
// and charges each unique blob once in the same transaction. Exact replay returns
// the original receipt; expiry, another uploader or substituted bytes cannot mint one.
func (t *Tenant) CommitBlobUpload(ctx context.Context, request VerifiedUploadCommit) (blob.Receipt, error) {
	return t.commitBlobUpload(ctx, request)
}

// AbortBlobUpload releases an uncommitted reservation after signing/transfer failure.
// It is idempotent for the same authorized uploader and cannot remove a committed
// blob or receipt. Retention releases expired reservations through its leased pass.
// All byte and object holds are released together, including across month changes.
// Physical staging cleanup belongs to the object backend after store settlement.
func (t *Tenant) AbortBlobUpload(ctx context.Context, authority blob.Authority, ticketID string) error {
	return t.abortBlobUpload(ctx, authority, ticketID)
}
