package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// DataController joins authorized data routes to transactional records and physical objects.
type DataController struct {
	Store            *store.Store
	Objects          blob.ObjectBackend
	ResolveTenant    func(context.Context, store.Team) (DataTransactions, error)
	ResolveAuthority func(context.Context, capability.Authorized) (blob.Authority, error)
	ReadBlob         func(context.Context, blob.Authority, blob.Digest) (blob.Blob, error)
	ChargeDownload   func(context.Context, blob.Authority, blob.Blob) error
	Access           capability.DataAccessChecker
	replicaID        string
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
type DataBinding struct {
	Authority blob.Authority
	Signer    blob.DataSigner
	Records   blob.RecordStore
}

// DataAuthority resolves live uploader identity and sealed data scope from persisted authority.
func (s *DataController) DataAuthority(ctx context.Context, authorized capability.Authorized) (blob.Authority, error) {
	if s == nil {
		return blob.Authority{}, missingEndpointPolicy()
	}
	if err := ctx.Err(); err != nil {
		return blob.Authority{}, err
	}
	resolve := s.ResolveAuthority
	if resolve == nil {
		resolve = s.persistedDataAuthority
	}
	authority, err := resolve(ctx, authorized)
	if err != nil {
		return blob.Authority{}, engineLookupError(err)
	}
	if err := validateDataAuthority(authorized, authority); err != nil {
		return blob.Authority{}, err
	}
	return authority, nil
}

// BindData supplies byte-signing and record operations for one authenticated subject.
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
