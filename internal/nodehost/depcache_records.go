package nodehost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"

	depcacheblob "github.com/sparkwing-dev/sparkwing/internal/blob/depcache"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/depcachekey"
)

// DepcacheRecords restores and saves declared directories through bound storage.
type DepcacheRecords struct {
	Bind    func(context.Context, NodeInput, blob.DepcacheKey) (JobUpload, error)
	Resolve func(context.Context, NodeInput, nodewire.CacheSpec) (string, error)
	Budget  blob.Budget
}

// CacheReporter receives one line per declared cache and attempt, at level info or warn.
type CacheReporter func(level, message string)

type directoryCache struct {
	declaration   nodewire.CacheSpec
	key           blob.DepcacheKey
	dir           string
	save          bool
	cold          string
	restoreFailed bool

	workspace, declared string
}

// Prepare restores caches once and saves successful cold directories after retries.
func (d DepcacheRecords) Prepare(ctx context.Context, input NodeInput, job nodewire.JobSpec, report CacheReporter) (ExecutorFinalizer, error) {
	if len(job.Caches) == 0 {
		return nil, nil
	}
	if input.Entry == nil || input.Entry.Directory == "" || input.Storage.Bytes == nil || input.Storage.Records == nil || d.Bind == nil {
		return nil, errors.New("cache layer requires a workspace and bound storage adapters")
	}
	if report == nil {
		// safety: only the executor's reporter holds the job-scoped masker, so lines are never
		// written unmasked to the host log.
		report = func(string, string) {}
	}
	workspace := sdkWorkingDirectory(input.Entry)
	caches := make([]directoryCache, 0, len(job.Caches))
	var uncached []string
	for _, declaration := range job.Caches {
		cache, err := d.prepare(ctx, input, declaration)
		if errors.Is(err, errNoCacheKeyFile) {
			uncached = append(uncached, fmt.Sprintf("%s: no key file (%s) in the checkout; running uncached", cacheLabel(declaration), checkoutKeyFiles(workspace, declaration.KeyFiles)))
			continue
		}
		if err != nil {
			uncached = append(uncached, cacheLabel(declaration)+": preparation failed; running uncached")
			continue
		}
		caches = append(caches, cache)
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	// safety: every outcome is reported here, after retries, because a new attempt clears the
	// job's annotations and only the finishing attempt holds authority to write them.
	return func(ctx context.Context, current NodeInput, result *runstate.NodeResult) error {
		for _, message := range uncached {
			report("warn", message)
		}
		for _, cache := range caches {
			prefix := fmt.Sprintf("%s: %s", cacheLabel(cache.declaration), cache.cold)
			level := "info"
			if cache.restoreFailed {
				level = "warn"
			}
			switch {
			case !cache.save:
				report(level, prefix)
				continue
			case result.Outcome != runstate.NodeSuccess:
				report(level, prefix+"; not saved because the job did not succeed")
				continue
			}
			size, err := d.save(ctx, current, cache)
			switch {
			case err != nil:
				report("warn", prefix+"; save failed, the next run starts cold")
			case size == 0:
				report(level, prefix+"; nothing to save, the cache directory is empty after the job")
			default:
				report(level, fmt.Sprintf("%s; saved %s", prefix, formatCacheBytes(size)))
			}
		}
		return context.Cause(ctx)
	}, nil
}

var errNoCacheKeyFile = errors.New("cache has no readable key file")

// safety: a resolver declaration's Path is never validated, and key files may name
// absolute paths, so only checkout-relative directory paths reach a user-visible line.
func cacheLabel(declaration nodewire.CacheSpec) string {
	path := filepath.Clean(declaration.Path)
	if declaration.Resolver == "" && declaration.Path != "" && filepath.IsLocal(path) && filepath.ToSlash(path) != declaration.Name {
		return fmt.Sprintf("cache %s (%s)", declaration.Name, filepath.ToSlash(path))
	}
	return "cache " + declaration.Name
}

func checkoutKeyFiles(workspace string, files []string) string {
	shown := make([]string, 0, len(files))
	for _, name := range files {
		if filepath.IsAbs(name) {
			relative, err := filepath.Rel(workspace, name)
			if err != nil {
				shown = append(shown, "a file outside the checkout")
				continue
			}
			name = relative
		}
		if name = filepath.Clean(name); !filepath.IsLocal(name) {
			shown = append(shown, "a file outside the checkout")
			continue
		}
		shown = append(shown, filepath.ToSlash(name))
	}
	return strings.Join(shown, ", ")
}

func formatCacheBytes(size int64) string {
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(size)/(1<<10))
	}
	return fmt.Sprintf("%d B", size)
}

func shortCacheKey(key string) string {
	key = strings.TrimPrefix(key, "dep-")
	if len(key) > 12 {
		return key[:12]
	}
	return key
}

func (d DepcacheRecords) prepare(ctx context.Context, input NodeInput, declaration nodewire.CacheSpec) (directoryCache, error) {
	key, err := directoryCacheKey(ctx, sdkWorkingDirectory(input.Entry), declaration)
	if err != nil {
		return directoryCache{}, err
	}
	cache := directoryCache{declaration: declaration, key: blob.DepcacheKey{Repo: input.Storage.Authority.Repo, Ref: input.Storage.Authority.Ref.Scope, Name: declaration.Name, Key: key}}
	if declaration.Resolver != "" {
		if d.Resolve == nil {
			return directoryCache{}, errors.New("cache directory resolver is unavailable")
		}
		cache.dir, err = d.Resolve(ctx, input, declaration)
		if err != nil {
			return directoryCache{}, err
		}
		if !filepath.IsAbs(cache.dir) {
			return directoryCache{}, errors.New("cache resolver returned a relative directory")
		}
	} else {
		if !filepath.IsLocal(declaration.Path) {
			return directoryCache{}, errors.New("cache path must be relative and stay inside the workspace")
		}
		cache.workspace, cache.declared = sdkWorkingDirectory(input.Entry), declaration.Path
		cache.dir = filepath.Join(cache.workspace, cache.declared)
	}
	empty, err := cache.empty()
	if err != nil {
		return cache, err
	}
	short := shortCacheKey(key)
	if !empty {
		cache.cold = fmt.Sprintf("present on this host (key %s); not restored or saved", short)
		return cache, nil
	}
	restored, err := d.restore(ctx, input, cache)
	switch {
	case err != nil:
		cache.cold, cache.restoreFailed = fmt.Sprintf("restore failed (key %s); ran cold", short), true
	case restored:
		cache.cold = fmt.Sprintf("hit (key %s); restored", short)
	default:
		cache.cold = fmt.Sprintf("miss (key %s)", short)
	}
	cache.save = !restored
	return cache, nil
}

func directoryCacheKey(ctx context.Context, workspace string, declaration nodewire.CacheSpec) (key string, err error) {
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
	return "", errNoCacheKeyFile
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
	reader, err := get(ctx, record.Digest)
	if err != nil {
		if reader != nil {
			err = errors.Join(err, reader.Close())
		}
		return false, err
	}
	parent, name, err := cache.open()
	if err != nil {
		return false, errors.Join(err, reader.Close())
	}
	defer parent.Close()
	if _, err := depcacheblob.RestoreArchiveIn(ctx, reader, parent, name); err != nil {
		return false, err
	}
	return true, nil
}

// safety: the host archives and overwrites this directory, so a declared path outside the
// workspace, or a symlink swapped in while the job runs, must not reach files only the host can.
// Every open goes through an os.Root, which refuses a path that resolves outside it.
func (c directoryCache) open() (parent *os.Root, name string, err error) {
	if c.workspace == "" {
		if err := os.MkdirAll(filepath.Dir(c.dir), 0o755); err != nil {
			return nil, "", err
		}
		parent, err = os.OpenRoot(filepath.Dir(c.dir))
		return parent, filepath.Base(c.dir), err
	}
	if !filepath.IsLocal(c.declared) {
		return nil, "", errors.New("cache path must be relative and stay inside the workspace")
	}
	parent, err = os.OpenRoot(c.workspace)
	return parent, filepath.Clean(c.declared), err
}

func (c directoryCache) empty() (bool, error) {
	parent, name, err := c.open()
	if err != nil {
		return false, err
	}
	defer parent.Close()
	dir, err := parent.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return len(entries) == 0, err
}
