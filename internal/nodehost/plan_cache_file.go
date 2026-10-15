package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// FilePlanCache holds private, recoverable declarations for one authorized scope.
// Lookup returns untrusted entries; ValidatePlanCacheHit remains mandatory before reuse.
type FilePlanCache struct {
	directory  string
	team       string
	repository runspec.RepoKey
}

// NewFilePlanCache binds a host-owned directory to an already authorized team/repository.
// Authorization belongs to the host; accepting a scope here grants no repository access.
func NewFilePlanCache(directory, team string, repository runspec.RepoKey) (*FilePlanCache, error) {
	if !filepath.IsAbs(directory) || team == "" || repository == "" || !utf8.ValidString(team) || !utf8.ValidString(string(repository)) {
		return nil, errors.New("plan cache requires an absolute directory and complete scope")
	}
	scope, err := json.Marshal([2]string{team, string(repository)})
	if err != nil {
		return nil, err
	}
	scopeDirectory := filepath.Join(directory, planCacheChecksum(scope))
	if err := fssecure.EnsureDir(scopeDirectory); err != nil {
		return nil, fmt.Errorf("create plan cache: %w", err)
	}
	if err := fssecure.SecurePrivateDir(scopeDirectory); err != nil {
		return nil, fmt.Errorf("protect plan cache: %w", err)
	}
	return &FilePlanCache{directory: scopeDirectory, team: team, repository: repository}, nil
}

// Lookup reports damaged files as misses and removes them before returning.
func (c *FilePlanCache) Lookup(ctx context.Context, key PlanCacheKey) (PlanCacheLookup, error) {
	if err := ctx.Err(); err != nil {
		return PlanCacheLookup{}, err
	}
	path, err := c.path(key)
	if err != nil {
		return PlanCacheLookup{MissReason: "invalid cache scope or key"}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return PlanCacheLookup{MissReason: "entry is absent"}, nil
	}
	if err != nil {
		return PlanCacheLookup{MissReason: "entry is unreadable"}, planCacheStorageError("read", err)
	}
	if err := ctx.Err(); err != nil {
		return PlanCacheLookup{}, err
	}
	var entry PlanCacheEntry
	const prefix = sha256.Size*2 + 1
	if len(data) < prefix || data[prefix-1] != '\n' || string(data[:prefix-1]) != planCacheChecksum(data[prefix:]) {
		return c.discard(ctx, key, "entry checksum mismatch")
	}
	if err := json.Unmarshal(data[prefix:], &entry); err != nil {
		return c.discard(ctx, key, "entry is malformed")
	}
	if entry.Key != key || planCacheEntryIdentity(entry) != nil {
		return c.discard(ctx, key, "entry identity or declarations are invalid")
	}
	return PlanCacheLookup{Entry: &entry}, nil
}

// Put persists declarations supplied by the validated planning path, never an accepted seal.
func (c *FilePlanCache) Put(ctx context.Context, entry PlanCacheEntry) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := c.path(entry.Key)
	if err != nil {
		return err
	}
	if err := planCacheEntryIdentity(entry); err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode plan cache: %w", err)
	}
	temporary, err := fssecure.MkdirPrivateTemp(c.directory, ".plan-")
	if err != nil {
		return fmt.Errorf("create plan cache entry: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(temporary); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove incomplete plan cache entry: %w", removeErr))
		}
	}()
	file, err := fssecure.OpenFile(filepath.Join(temporary, "entry"), os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return fmt.Errorf("create plan cache entry: %w", err)
	}
	_, writeErr := file.Write(append([]byte(planCacheChecksum(data)+"\n"), data...))
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("write plan cache entry: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return planCacheStorageError("publish", err)
	}
	return nil
}

// Invalidate makes a missing entry harmless while reporting failed removal.
func (c *FilePlanCache) Invalidate(ctx context.Context, key PlanCacheKey, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := c.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return planCacheStorageError("invalidate", err)
	}
	return nil
}

func (c *FilePlanCache) discard(ctx context.Context, key PlanCacheKey, reason string) (PlanCacheLookup, error) {
	return PlanCacheLookup{MissReason: reason}, c.Invalidate(ctx, key, reason)
}

func (c *FilePlanCache) path(key PlanCacheKey) (string, error) {
	if key.Team != c.team || key.Repository != c.repository || key.Version <= 0 || !planCacheDigestValid(key.Digest) {
		return "", errors.New("plan cache key is invalid or outside its authorized scope")
	}
	return filepath.Join(c.directory, fmt.Sprintf("%d-%s", key.Version, key.Digest)), nil
}

func planCacheEntryIdentity(entry PlanCacheEntry) error {
	key, eligible, err := PlanCacheIdentity(entry.Inputs)
	if err != nil {
		return err
	}
	if !eligible.Eligible || key != entry.Key {
		return errors.New("plan cache entry does not match its inputs")
	}
	if !planCacheDigestValid(entry.PlanHash) || !json.Valid(entry.Plan) {
		return errors.New("plan cache entry has invalid plan bytes or hash")
	}
	return nil
}

func planCacheChecksum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func planCacheStorageError(operation string, err error) error {
	// safety: filenames contain hashes of secret-bearing inputs and must not enter warnings.
	var pathErr *os.PathError
	var linkErr *os.LinkError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	} else if errors.As(err, &linkErr) {
		err = linkErr.Err
	}
	return fmt.Errorf("%s plan cache entry: %w", operation, err)
}

var _ PlanCache = (*FilePlanCache)(nil)
