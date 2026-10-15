package blob

import (
	"context"
	"fmt"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type remoteCache struct {
	options  RemoteCacheOptions
	store    Store
	mu       sync.Mutex
	readable map[Digest]bool
}

// OpenRemoteCache opens read-only storage with a repo-bound user authority.
func OpenRemoteCache(options RemoteCacheOptions, store Store) (RemoteCache, error) {
	if options.Profile == "" || options.AuthorityRef == "" || options.Repo == "" || options.Records == nil || store == nil {
		return nil, fmt.Errorf("remote cache requires a profile, repository authority, records and signed store")
	}
	return &remoteCache{options: options, store: store, readable: make(map[Digest]bool)}, nil
}

func (c *remoteCache) authority() Authority {
	return Authority{CapabilityRef: c.options.AuthorityRef, Repo: c.options.Repo}
}

func (c *remoteCache) lookup(ctx context.Context, key RecordKey, budget Budget) (RecordLookupResult, error) {
	if key.Scope.Repo != c.options.Repo {
		return RecordLookupResult{}, ErrDigestNotReadable
	}
	if budget.Deadline.IsZero() {
		return RecordLookupResult{}, ErrBudget
	}
	ctx, cancel := context.WithDeadline(ctx, budget.Deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return RecordLookupResult{}, budgetError(err)
	}
	result, err := c.options.Records.LookupRecord(ctx, RecordLookup{Authority: c.authority(), Key: key, Budget: budget, VerifiedDefaultOnly: true})
	if err != nil || !result.Found {
		return RecordLookupResult{}, err
	}
	r := result.Record
	f := r.RefFacts
	if r.Key.Kind != key.Kind || r.Key.Scope.Repo != c.options.Repo || !f.Scope.Verified || !f.DefaultBranchVerified || f.AppDeliveryKey == "" || f.SHA == "" || f.DefaultBranch == "" || f.Scope.Ref != f.DefaultBranch || r.Key.Scope.Ref != f.Scope {
		return RecordLookupResult{}, ErrDigestNotReadable
	}
	if r.Key.Scope.Pipeline != key.Scope.Pipeline || r.Key.Scope.Job != key.Scope.Job || r.Key.Scope.Key != key.Scope.Key || r.Key.Name != key.Name {
		return RecordLookupResult{}, ErrDigestNotReadable
	}
	digests := []Digest{r.Digest}
	if r.ManifestDigest.Set {
		digests = append(digests, r.ManifestDigest.Value)
	}
	for _, m := range result.Members {
		if !r.ManifestDigest.Set || m.Team != r.Key.Team || m.ManifestDigest != r.ManifestDigest.Value {
			return RecordLookupResult{}, ErrDigestNotReadable
		}
		digests = append(digests, m.Digest)
	}
	if key.Kind == KindMemo {
		_, err = readMemoBytes(ctx, c.store, c.authority(), r.Key.Team, MemoHit{
			Found: true, Output: runspec.Optional[Digest]{Set: true, Value: r.Digest},
			Manifest: r.ManifestDigest, Members: result.Members,
		})
	} else {
		for _, d := range digests {
			if _, err = verifyMemoBlob(ctx, c.store, c.authority(), d); err != nil {
				break
			}
		}
	}
	if err != nil {
		return RecordLookupResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return RecordLookupResult{}, budgetError(err)
	}
	c.mu.Lock()
	for _, d := range digests {
		c.readable[d] = true
	}
	c.mu.Unlock()
	return result, nil
}

func (c *remoteCache) Memo(ctx context.Context, key MemoKey, budget Budget) (MemoHit, error) {
	if key.Repo != c.options.Repo {
		return MemoHit{}, ErrDigestNotReadable
	}
	if !validDigest(Digest(key.Hash)) {
		return MemoHit{}, fmt.Errorf("invalid memo hash")
	}
	if _, err := namespace(string(key.Repo), key.Pipeline, key.Job, string(key.Hash)); err != nil {
		return MemoHit{}, err
	}
	result, err := c.lookup(ctx, RecordKey{Kind: KindMemo, Name: MemoRecordName, Scope: Scope{Repo: key.Repo, Pipeline: key.Pipeline, Job: key.Job, Key: string(key.Hash)}}, budget)
	if err != nil || !result.Found {
		return MemoHit{}, ctx.Err()
	}
	return MemoHit{Found: true, Output: runspec.Optional[Digest]{Set: true, Value: result.Record.Digest}, Manifest: result.Record.ManifestDigest, Members: result.Members, ReadSet: result.ReadSet}, nil
}

func (c *remoteCache) Depcache(ctx context.Context, key DepcacheKey, budget Budget) (Record, error) {
	if key.Repo != c.options.Repo {
		return Record{}, ErrDigestNotReadable
	}
	if _, err := namespace(string(key.Repo), key.Name, key.Key); err != nil {
		return Record{}, err
	}
	result, err := c.lookup(ctx, RecordKey{Kind: KindDepcache, Scope: Scope{Repo: key.Repo, Key: key.Key}, Name: key.Name}, budget)
	if err != nil || !result.Found {
		if err := ctx.Err(); err != nil {
			return Record{}, err
		}
		return Record{}, ErrNotFound
	}
	return result.Record, nil
}

func (c *remoteCache) Artifact(ctx context.Context, digest Digest) (VerifiedReader, error) {
	c.mu.Lock()
	allowed := c.readable[digest]
	c.mu.Unlock()
	if !allowed {
		return nil, ErrDigestNotReadable
	}
	return c.store.Get(ctx, GetRequest{Authority: c.authority(), Digest: digest})
}
