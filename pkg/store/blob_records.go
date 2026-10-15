package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// CommitRecord checks all member existence and reference authority before making any digest reachable.
func (t *Tenant) CommitRecord(ctx context.Context, request blob.CommitRecordRequest) (blob.Record, error) {
	return t.BlobRecords(nil).CommitRecord(ctx, request)
}

// CopyMemo creates destination output and manifest edges atomically from a server-scoped cache hit.
func (t *Tenant) CopyMemo(ctx context.Context, request blob.CopyMemoRequest) (blob.MemoHit, error) {
	return t.BlobRecords(nil).CopyMemo(ctx, request)
}

// LookupRecord adds no authority merely because a key exists; all returned bytes must be in the caller's read set.
func (t *Tenant) LookupRecord(ctx context.Context, request blob.RecordLookup) (blob.RecordLookupResult, error) {
	return t.BlobRecords(nil).LookupRecord(ctx, request)
}

// LookupMemo applies the claim's repo/ref/pipeline scope and the verified default-branch fallback.
func (t *Tenant) LookupMemo(ctx context.Context, request blob.MemoLookup) (blob.MemoHit, error) {
	return t.BlobRecords(nil).LookupMemo(ctx, request)
}

// LookupDepcache follows the single restore-order table and runs cold on absent or invalid bytes.
func (t *Tenant) LookupDepcache(ctx context.Context, request blob.DepcacheLookup) (blob.DepcacheHit, error) {
	return t.BlobRecords(nil).LookupDepcache(ctx, request)
}

var _ blob.RecordStore = (*Tenant)(nil)
