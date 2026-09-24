package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StoragePassEvery is how often one replica lists the cache's and the logs
// service's buckets to reconcile every team's stored bytes.
const StoragePassEvery = time.Hour

const cacheObjectMaxAge = store.DirectCacheMaxAge

// CacheObjectMaxAge is how long the storage pass keeps cache objects in every
// team namespace and the operator token's root namespace.
func CacheObjectMaxAge(_ string) time.Duration {
	return cacheObjectMaxAge
}

type storagePass struct {
	stores map[store.StorageKind]*teamblob.Store

	mu      sync.Mutex
	lastRun time.Time
	lastErr error
}

// WithStoragePass names the buckets the storage pass lists: the cache's
// blob store, which it also expires, and the logs service's archive. A nil
// store is not listed.
func (s *Server) WithStoragePass(cache, logs *teamblob.Store) *Server {
	p := &storagePass{stores: map[store.StorageKind]*teamblob.Store{}}
	if cache != nil {
		p.stores[store.StorageCache] = cache
	}
	if logs != nil {
		p.stores[store.StorageLogs] = logs
	}
	s.storagePass = p
	return s
}

// RunStoragePass runs one storage pass now unless another replica ran one
// within [StoragePassEvery], and reports whether it ran.
func (s *Server) RunStoragePass(ctx context.Context) (bool, error) {
	p := s.storagePass
	if p == nil || s.store == nil {
		return false, nil
	}
	ran, err := s.store.RunStoragePassLeased(ctx, s.measureHolder(), StoragePassEvery, StoragePassEvery,
		func(ctx context.Context) error { return s.storagePassOnce(ctx, p) })
	if ran {
		p.mu.Lock()
		p.lastRun, p.lastErr = time.Now(), err
		p.mu.Unlock()
	}
	return ran, err
}

// safety: a store whose listing failed keeps its counts rather than taking a partial
// listing for the whole.
func (s *Server) storagePassOnce(ctx context.Context, p *storagePass) error {
	now := time.Now()
	var errs []error
	if _, err := s.store.ReleaseExpiredStorage(ctx, now); err != nil {
		errs = append(errs, fmt.Errorf("release expired reservations: %w", err))
	}
	if _, err := s.store.PruneExpiredUploads(ctx, now); err != nil {
		errs = append(errs, fmt.Errorf("prune expired uploads: %w", err))
	}
	for kind, bucket := range p.stores {
		// safety: the marks are read before the listing, so what is committed
		// while it runs is added back to what it finds.
		marks, err := s.store.StorageMarks(ctx, kind)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s marks: %w", kind, err))
			continue
		}
		m, err := bucket.Measure(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("list the %s bucket: %w", kind, err))
			continue
		}
		listed := make(map[store.Team]int64, len(m.Teams))
		for team, t := range m.Teams {
			listed[store.Team(team)] = t.Bytes
		}
		if err := s.store.ReconcileStorage(ctx, kind, listed, marks, now); err != nil {
			errs = append(errs, fmt.Errorf("reconcile %s: %w", kind, err))
			continue
		}
		if kind == store.StorageCache {
			if _, err := s.store.PruneExpiredCacheObjects(ctx, now); err != nil {
				errs = append(errs, fmt.Errorf("prune expired cache object rows: %w", err))
			}
		}
		s.logger.Info("storage pass", "store", string(kind), "teams", len(m.Teams),
			"expired_objects", m.Expired.Objects, "expired_bytes", m.Expired.Bytes)
	}
	utc := now.UTC()
	if _, err := s.store.PruneDownloadDays(ctx, utc.AddDate(0, 0, -7).Format("2006-01-02")); err != nil {
		errs = append(errs, fmt.Errorf("prune download days: %w", err))
	}
	if _, err := s.store.PruneEgressTotals(ctx, utc.AddDate(0, -1, 0).Format("2006-01")); err != nil {
		errs = append(errs, fmt.Errorf("prune egress totals: %w", err))
	}
	return errors.Join(errs...)
}

func (s *Server) runStoragePass(ctx context.Context) {
	if s.storagePass == nil {
		return
	}
	run := func() {
		if _, err := s.RunStoragePass(ctx); err != nil && ctx.Err() == nil {
			s.logger.Error("storage pass", "err", err)
		}
	}
	run()
	// perf: the lease holds the pass to once a window across replicas, so
	// checking four times a window costs one meta row read each and keeps a
	// replica that started late from waiting out two windows.
	t := time.NewTicker(StoragePassEvery / 4)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

func (s *Server) storagePassHealth() (map[string]any, []string) {
	p := s.storagePass
	if p == nil {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	summary := map[string]any{"last_run": p.lastRun, "ok": p.lastErr == nil}
	if p.lastErr == nil {
		return summary, nil
	}
	return summary, []string{"storage pass: the last pass could not list or reconcile every store, so those counts " +
		"stand as they were; the controller log names the error"}
}
