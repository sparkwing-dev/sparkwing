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
	panic("unimplemented: c2-data")
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
	panic("unimplemented: c2-data")
}
