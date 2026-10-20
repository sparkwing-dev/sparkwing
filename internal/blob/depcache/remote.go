package depcacheblob

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// NewRemoteReader restores verified-default archives through the opted-in user
// cache. Use depcache.WithRemoteReader to retain local saves without remote writes.
func NewRemoteReader(cache blob.RemoteCache, repo runspec.RepoKey, budget blob.Budget) (depcache.Reader, error) {
	if cache == nil || repo == "" || budget.Deadline.IsZero() {
		return nil, fmt.Errorf("remote dependency cache requires a cache, repository and budget")
	}
	return remoteReader{cache: cache, repo: repo, budget: budget}, nil
}

type remoteReader struct {
	cache  blob.RemoteCache
	repo   runspec.RepoKey
	budget blob.Budget
}

func (r remoteReader) lookup(ctx context.Context, key string) (blob.Record, error) {
	if err := ctx.Err(); err != nil {
		return blob.Record{}, err
	}
	record, err := r.cache.Depcache(ctx, blob.DepcacheKey{Repo: r.repo, Name: key, Key: key}, r.budget)
	if ctx.Err() != nil {
		return blob.Record{}, ctx.Err()
	}
	return record, err
}

func (r remoteReader) Exists(ctx context.Context, key string) (bool, error) {
	ctx, cancel := context.WithDeadline(ctx, r.budget.Deadline)
	defer cancel()
	_, err := r.lookup(ctx, key)
	if errors.Is(err, blob.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (r remoteReader) Fetch(ctx context.Context, key, dir string) (int64, error) {
	ctx, cancel := context.WithDeadline(ctx, r.budget.Deadline)
	defer cancel()
	record, err := r.lookup(ctx, key)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	body, err := r.cache.Artifact(ctx, record.Digest)
	if err != nil {
		return 0, err
	}
	return restoreArchive(ctx, body, dir)
}
