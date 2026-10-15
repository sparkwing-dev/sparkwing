package nodehost

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/depcachekey"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// DepcacheRecords restores and saves declared directories through bound storage.
type DepcacheRecords struct {
	Bind    func(context.Context, NodeInput, blob.DepcacheKey) (JobUpload, error)
	Resolve func(context.Context, NodeInput, nodeproto.CacheSpec) (string, error)
	Budget  blob.Budget
	Warn    func(string)
}

type directoryCache struct {
	key  blob.DepcacheKey
	dir  string
	save bool
}

// Prepare restores caches once and saves successful cold directories after retries.
func (d DepcacheRecords) Prepare(ctx context.Context, layer LayerSpec, input NodeInput, job nodeproto.JobSpec) (ExecutorFinalizer, error) {
	if layer.Name != "caches" || len(job.Caches) == 0 {
		return nil, nil
	}
	if input.Entry == nil || input.Entry.Directory == "" || input.Storage.Bytes == nil || input.Storage.Records == nil || d.Bind == nil {
		return nil, errors.New("cache layer requires a workspace and bound storage adapters")
	}
	caches := make([]directoryCache, 0, len(job.Caches))
	for _, declaration := range job.Caches {
		cache, err := d.prepare(ctx, input, declaration)
		if err != nil {
			d.warning("cache preparation failed; running uncached")
			continue
		}
		caches = append(caches, cache)
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return func(ctx context.Context, current NodeInput, result *store.NodeResult) error {
		if result.Outcome != store.NodeSuccess {
			return nil
		}
		for _, cache := range caches {
			if cache.save {
				if err := d.save(ctx, current, cache); err != nil {
					d.warning("cache save failed; next run will start cold")
				}
			}
		}
		return context.Cause(ctx)
	}, nil
}

func (d DepcacheRecords) prepare(ctx context.Context, input NodeInput, declaration nodeproto.CacheSpec) (directoryCache, error) {
	key, err := directoryCacheKey(ctx, input.Entry.Directory, declaration)
	if err != nil {
		return directoryCache{}, err
	}
	dir := declaration.Path
	if declaration.Resolver != "" {
		if d.Resolve == nil {
			return directoryCache{}, errors.New("cache directory resolver is unavailable")
		}
		dir, err = d.Resolve(ctx, input, declaration)
		if err != nil {
			return directoryCache{}, err
		}
	}
	if dir == "" {
		return directoryCache{}, errors.New("cache directory is empty")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(input.Entry.Directory, dir)
	}
	cache := directoryCache{dir: dir, key: blob.DepcacheKey{Repo: input.Storage.Authority.Repo, Ref: input.Storage.Authority.Ref.Scope, Name: declaration.Name, Key: key}}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return cache, err
	}
	if len(entries) != 0 {
		return cache, nil
	}
	restored, err := d.restore(ctx, input, cache)
	if err != nil {
		d.warning("cache restore failed; running cold")
	}
	cache.save = !restored
	return cache, nil
}

func directoryCacheKey(ctx context.Context, workspace string, declaration nodeproto.CacheSpec) (key string, err error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	for _, name := range declaration.KeyFiles {
		if err := context.Cause(ctx); err != nil {
			return "", err
		}
		if filepath.IsAbs(name) {
			name, err = filepath.Rel(workspace, name)
			if err != nil {
				return "", err
			}
		}
		file, err := root.Open(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			closeErr := file.Close()
			if statErr != nil || closeErr != nil {
				return "", errors.Join(statErr, closeErr)
			}
			continue
		}
		value, readErr := io.ReadAll(file)
		if err := errors.Join(readErr, file.Close(), context.Cause(ctx)); err != nil {
			return "", err
		}
		return depcachekey.Bytes(declaration.Name, declaration.KeyScope, value)
	}
	return "", errors.New("cache has no readable key file")
}

func (d DepcacheRecords) restore(ctx context.Context, input NodeInput, cache directoryCache) (bool, error) {
	if !d.Budget.Deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, d.Budget.Deadline)
		defer cancel()
	}
	if err := context.Cause(ctx); err != nil {
		return false, err
	}
	lookup := blob.DepcacheLookup{Authority: input.Storage.Authority, Key: cache.key, Budget: d.Budget}
	hit, err := input.Storage.Records.LookupDepcache(ctx, lookup)
	if err != nil {
		return false, err
	}
	var record blob.Record
	get := func(ctx context.Context, digest blob.Digest) (blob.VerifiedReader, error) {
		return input.Storage.Bytes.Get(ctx, blob.GetRequest{Authority: input.Storage.Authority, Digest: digest})
	}
	if hit.Found {
		record = hit.Record
		if record.Key.Team != input.Storage.Authority.Attempt.Value.Team {
			return false, errors.New("cache lookup changed its bound team")
		}
	} else if input.Storage.RemoteCache != nil {
		record, err = input.Storage.RemoteCache.Depcache(ctx, cache.key, d.Budget)
		if err != nil {
			return false, err
		}
		get = input.Storage.RemoteCache.Artifact
	} else {
		return false, nil
	}
	if record.Key.Team == "" || record.Key.Kind != blob.KindDepcache || record.Key.Scope.Repo != cache.key.Repo || record.Key.Scope.Key != cache.key.Key || record.Key.Name != cache.key.Name || !artifactDigestValid(record.Digest) {
		return false, errors.New("cache lookup returned a different record")
	}
	if err := restoreDirectory(ctx, cache.dir, func() (blob.VerifiedReader, error) { return get(ctx, record.Digest) }); err != nil {
		return false, err
	}
	return true, nil
}

func (d DepcacheRecords) warning(message string) {
	if d.Warn != nil {
		d.Warn(message)
	} else {
		slog.Warn(message)
	}
}
