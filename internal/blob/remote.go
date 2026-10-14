package blob

import (
	"context"
	"fmt"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"io"
	"sync"
)

type remoteCache struct {
	options  RemoteCacheOptions
	store    Store
	mu       sync.Mutex
	readable map[Digest]bool
}

func openRemoteCache(options RemoteCacheOptions, store Store) (RemoteCache, error) {
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
	seen := make(map[Digest]bool, len(digests))
	for _, d := range digests {
		if seen[d] {
			continue
		}
		seen[d] = true
		if err := c.verify(ctx, d); err != nil {
			return RecordLookupResult{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return RecordLookupResult{}, budgetError(err)
	}
	c.mu.Lock()
	for d := range seen {
		c.readable[d] = true
	}
	c.mu.Unlock()
	return result, nil
}

func (c *remoteCache) verify(ctx context.Context, d Digest) error {
	r, err := c.store.Get(ctx, GetRequest{Authority: c.authority(), Digest: d})
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, r)
	cerr := r.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if !r.Verified() {
		return ErrIntegrity
	}
	return nil
}

func (c *remoteCache) Memo(ctx context.Context, key MemoKey, budget Budget) (MemoHit, error) {
	if key.Repo != c.options.Repo {
		return MemoHit{}, ErrDigestNotReadable
	}
	if _, err := MemoNamespace(key); err != nil {
		return MemoHit{}, err
	}
	result, err := c.lookup(ctx, RecordKey{Kind: KindMemo, Scope: Scope{Repo: key.Repo, Pipeline: key.Pipeline, Job: key.Job, Key: string(key.Hash)}}, budget)
	if err != nil || !result.Found {
		return MemoHit{}, nil
	}
	return MemoHit{Found: true, Output: runspec.Optional[Digest]{Set: true, Value: result.Record.Digest}, Manifest: result.Record.ManifestDigest, Members: result.Members, ReadSet: result.ReadSet}, nil
}

func (c *remoteCache) Depcache(ctx context.Context, key DepcacheKey, budget Budget) (Record, error) {
	if key.Repo != c.options.Repo {
		return Record{}, ErrDigestNotReadable
	}
	if _, err := DepcacheNamespace(key); err != nil {
		return Record{}, err
	}
	result, err := c.lookup(ctx, RecordKey{Kind: KindDepcache, Scope: Scope{Repo: key.Repo, Key: key.Key}, Name: key.Name}, budget)
	if err != nil || !result.Found {
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
