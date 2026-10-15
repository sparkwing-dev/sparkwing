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

type storagePass struct {
	stores map[store.StorageKind]*teamblob.Store

	mu      sync.Mutex
	lastRun time.Time
	lastErr error
}

// WithStoragePass names the bucket listed beside the node log segments.
// A nil store is not listed. Blob lifetime follows record reachability.
func (s *Server) WithStoragePass(cache *teamblob.Store) *Server {
	p := &storagePass{stores: map[store.StorageKind]*teamblob.Store{}}
	if cache != nil {
		p.stores[store.StorageCache] = cache
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
	for _, kind := range []store.StorageKind{store.StorageCache, store.StorageLogs} {
		bucket := p.stores[kind]
		var segments logObjectStore
		if kind == store.StorageLogs {
			segments = s.logObjects()
		}
		if bucket == nil && segments == nil {
			continue
		}
		// safety: the marks are read before the listing, so what is committed
		// while it runs is added back to what it finds.
		marks, err := s.store.StorageMarks(ctx, kind)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s marks: %w", kind, err))
			continue
		}
		listed := map[store.Team]int64{}
		var m teamblob.Measurement
		if bucket != nil {
			if m, err = bucket.Measure(ctx); err != nil {
				errs = append(errs, fmt.Errorf("list the %s bucket: %w", kind, err))
				continue
			}
			for team, t := range m.Teams {
				listed[store.Team(team)] = t.Bytes
			}
		}
		if segments != nil {
			counted, err := segments.measure(ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("list the log segments: %w", err))
				continue
			}
			for team, n := range counted {
				listed[team] += n
			}
		}
		if err := s.store.ReconcileStorage(ctx, kind, listed, marks, now); err != nil {
			errs = append(errs, fmt.Errorf("reconcile %s: %w", kind, err))
			continue
		}
		if kind == store.StorageLogs {
			if err := s.pruneLogsOverShare(ctx, segments, now); err != nil {
				errs = append(errs, fmt.Errorf("prune logs over a free share: %w", err))
			}
		}
		s.logger.Info("storage pass", "store", string(kind), "teams", len(listed),
			"expired_objects", m.Expired.Objects, "expired_bytes", m.Expired.Bytes)
	}
	utc := now.UTC()
	if _, err := s.store.PruneDownloadDays(ctx, utc.AddDate(0, 0, -7).Format("2006-01-02")); err != nil {
		errs = append(errs, fmt.Errorf("prune download days: %w", err))
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

// safety: a free team's log writes are granted past its share, so this pass is
// what holds it there, about an hour behind: it deletes the segments of the
// team's least recently written finished runs until the team is back under,
// and never a run still going.
func (s *Server) pruneLogsOverShare(ctx context.Context, segments logObjectStore, now time.Time) error {
	over, err := s.store.TeamsOverFreeLogShare(ctx)
	if err != nil || len(over) == 0 {
		return err
	}
	var errs []error
	for _, t := range over {
		freed, err := s.pruneTeamSegments(ctx, segments, t)
		errs = append(errs, err)
		if freed > 0 {
			errs = append(errs, s.store.CommitStorage(ctx, store.StorageCommit{
				Team: t.Team, Kind: store.StorageLogs, Bytes: -freed, Now: now,
			}))
			s.logger.Info("pruned logs over a free share", "team", string(t.Team), "freed_bytes", freed)
		}
	}
	return errors.Join(errs...)
}

func (s *Server) pruneTeamSegments(ctx context.Context, segments logObjectStore, t store.TeamOverShare) (int64, error) {
	if segments == nil {
		return 0, nil
	}
	var freed int64
	for freed < t.OverBytes {
		recs, err := s.store.PrunableNodeLogs(ctx, t.Team, 100)
		if err != nil || len(recs) == 0 {
			return freed, err
		}
		for _, rec := range recs {
			if freed >= t.OverBytes {
				break
			}
			run, err := s.store.RunNodeLogs(ctx, t.Team, rec.RunID)
			if err != nil {
				return freed, err
			}
			if len(run) == 0 {
				continue
			}
			deleted, err := s.deleteRunSegments(ctx, segments, t.Team, rec.RunID)
			freed += deleted
			if err != nil {
				return freed, fmt.Errorf("delete log segments of run %s: %w", rec.RunID, err)
			}
		}
	}
	return freed, nil
}
