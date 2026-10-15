package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// DataController joins authenticated data routes to transactional records and
// verified physical objects. It serves the same contract for S3 and filesystem
// storage; filesystem transfer endpoints use signed-object authentication only.
type DataController struct {
	Store   *store.Store
	Objects blob.ObjectBackend
	// ResolveTenant defaults to Store.ForTeam and preserves the store transaction contracts.
	ResolveTenant func(context.Context, store.Team) (DataTransactions, error)
	// ResolveAuthority rechecks ancestry, membership and liveness, then reads sealed scope
	// and the exact uploader from persisted facts. It runs on every bound operation.
	// A missing resolver fails closed; a capability's asserted data scope is insufficient.
	// Its opaque references retain the input authority kind, including sessions.
	ResolveAuthority func(context.Context, capability.Authorized) (blob.Authority, error)
	// ReadBlob returns live existence and size after CheckDataAccess grants a read edge.
	// It returns blob.ErrNotFound for missing or tombstoned bytes and never creates an edge.
	ReadBlob func(context.Context, blob.Authority, blob.Digest) (blob.Blob, error)
	// ChargeDownload atomically applies the configured daily byte cap before signing.
	ChargeDownload func(context.Context, blob.Authority, blob.Blob) error
	Access         capability.DataAccessChecker
}

// DataTransactions preserves C1's upload lifecycle and atomic record operations.
type DataTransactions interface {
	blob.RecordStore
	ReserveBlobUpload(context.Context, store.UploadReservationRequest) (store.UploadReservation, error)
	LookupBlobUpload(context.Context, blob.Authority, string) (store.UploadReservation, error)
	CommitBlobUpload(context.Context, store.VerifiedUploadCommit) (blob.Receipt, error)
	AbortBlobUpload(context.Context, blob.Authority, string) error
}

// DataBinding is private host/adapter plumbing, never a wire response or seal.
// Signer and Records retain only the authenticated authority reference and
// reauthorize each operation under capability.DataPolicies and the route policy.
// Caller-supplied Authority/Identity fields are selectors to compare, never proof.
// Remote transports implement these same interfaces through the existing /data
// routes and keep reusable credentials out of signed-object headers and bodies.
type DataBinding struct {
	Authority blob.Authority
	Signer    blob.DataSigner
	Records   blob.RecordStore
}

// DataAuthority resolves uploader identity, repo/ref scope and principal from
// authenticated authority plus persisted run/attempt facts. Upload is populated
// only for an authorized job, live owner unit or publication; ordinary repo-read
// authority cannot acquire an uploader identity. No caller trust flag is copied.
func (s *DataController) DataAuthority(ctx context.Context, authorized capability.Authorized) (blob.Authority, error) {
	if s == nil || s.ResolveAuthority == nil {
		return blob.Authority{}, missingEndpointPolicy()
	}
	if err := ctx.Err(); err != nil {
		return blob.Authority{}, err
	}
	authority, err := s.ResolveAuthority(ctx, authorized)
	if err != nil {
		return blob.Authority{}, engineLookupError(err)
	}
	if err := validateDataAuthority(authorized, authority); err != nil {
		return blob.Authority{}, err
	}
	return authority, nil
}

// BindData supplies byte-signing and record operations for one authenticated subject.
// Upload authorizes and reserves before signing; signing failure aborts the reservation.
// Commit resolves the immutable ticket, reauthorizes, verifies/publishes its object,
// then calls CommitBlobUpload, which rechecks authority transactionally. Staging is
// deleted only after settlement. Failed settlement grants no receipt or read edge.
// Downloads and HasMany check the live read set; record operations check every edge.
// Verified-default-only lookup is also forced by DataRemoteCache scope, so a false
// request flag cannot broaden a remote-cache credential. Local cache reads never write.
func (s *DataController) BindData(ctx context.Context, authorized capability.Authorized) (DataBinding, error) {
	authority, err := s.DataAuthority(ctx, authorized)
	if err != nil {
		return DataBinding{}, err
	}
	if s.Access == nil {
		return DataBinding{}, missingEndpointPolicy()
	}
	bound := &boundData{controller: s, authorized: authorized, authority: authority}
	return DataBinding{Authority: authority, Signer: bound, Records: bound}, nil
}

var _ DataTransactions = (*store.Tenant)(nil)
