package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StoragePassEvery is how often one replica lists the cache's and the logs
// service's buckets to reconcile every team's stored bytes.
const StoragePassEvery = time.Hour

const cacheObjectMaxAge = store.DirectCacheMaxAge

// CacheObjectMaxAge is how long the storage pass keeps cache objects in every
// team namespace and the operator token's root namespace.
func CacheObjectMaxAge(team string) time.Duration {
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
	if err := s.pruneExpiredOutputs(ctx, now); err != nil {
		errs = append(errs, fmt.Errorf("prune expired outputs: %w", err))
	}
	for kind, bucket := range p.stores {
		if kind == store.StorageCache {
			expired, err := s.store.ClaimExpiredSourceBundles(ctx, now)
			if err != nil {
				errs = append(errs, fmt.Errorf("list expired source bundles: %w", err))
			} else {
				for _, source := range expired {
					if err := bucket.Delete(ctx, string(source.Team), "local/"+source.Key); err != nil {
						errs = append(errs, fmt.Errorf("delete source bundle %s: %w", source.ID, err))
						continue
					}
					if err := s.store.DeleteSourceBundleRows(ctx, source); err != nil {
						errs = append(errs, fmt.Errorf("prune source bundle %s: %w", source.ID, err))
					}
				}
			}
		}
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
		if kind == store.StorageLogs {
			if err := s.pruneLogsOverShare(ctx, bucket, now); err != nil {
				errs = append(errs, fmt.Errorf("prune logs over a free share: %w", err))
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

// safety: a free team's log writes are granted past its share, so this pass is
// what holds it there, about an hour behind: it deletes the team's least
// recently written finished runs' logs through the logs service until the
// team is back under, and never a run still going.
func (s *Server) pruneLogsOverShare(ctx context.Context, bucket *teamblob.Store, now time.Time) error {
	over, err := s.store.TeamsOverFreeLogShare(ctx)
	if err != nil || len(over) == 0 {
		return err
	}
	if s.teamStorage.LogsURL == "" {
		return errors.New("teams are past their log share and no logs service is configured to prune them")
	}
	if err := s.checkLogsDeleteToken(now); err != nil {
		return err
	}
	var errs []error
	for _, t := range over {
		freed, err := s.pruneTeamLogs(ctx, bucket, t)
		if freed > 0 {
			errs = append(errs, s.store.CommitStorage(ctx, store.StorageCommit{
				Team: t.Team, Kind: store.StorageLogs, Bytes: -freed, Now: now,
			}))
			s.logger.Info("pruned logs over a free share", "team", string(t.Team), "freed_bytes", freed)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Server) pruneTeamLogs(ctx context.Context, bucket *teamblob.Store, t store.TeamOverShare) (int64, error) {
	objs, err := bucket.List(ctx, string(t.Team), "runs/")
	if err != nil {
		return 0, err
	}
	type archivedRun struct {
		id   string
		last time.Time
	}
	byID := map[string]*archivedRun{}
	for _, o := range objs {
		id, _, ok := strings.Cut(strings.TrimPrefix(o.Rel, "runs/"), "/")
		if !ok {
			continue
		}
		r := byID[id]
		if r == nil {
			r = &archivedRun{id: id}
			byID[id] = r
		}
		if o.LastModified.After(r.last) {
			r.last = o.LastModified
		}
	}
	runs := slices.SortedFunc(maps.Values(byID), func(a, b *archivedRun) int { return a.last.Compare(b.last) })
	base := strings.TrimRight(s.teamStorage.LogsURL, "/") + "/api/v1/logs/"
	var freed int64
	for _, r := range runs {
		if freed >= t.OverBytes {
			break
		}
		finished, err := s.store.RunFinished(ctx, t.Team, r.id)
		if err != nil {
			return freed, err
		}
		if !finished {
			continue
		}
		deleted, err := deleteRunLogs(ctx, base+url.PathEscape(r.id), s.teamStorage.LogsToken)
		if err != nil {
			return freed, fmt.Errorf("delete logs of run %s: %w", r.id, err)
		}
		freed += deleted
	}
	return freed, nil
}

// safety: only what the logs service says it removed from the archive is
// taken off the team's count; a refusal or an answer naming nothing frees
// nothing, and the next pass's listing corrects any count left high.
func deleteRunLogs(ctx context.Context, target, bearer string) (int64, error) {
	// #nosec G704 -- the origin is operator configuration; the id is an escaped segment
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	// #nosec G704 -- the request keeps the operator-configured origin
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, errors.Join(fmt.Errorf("%d %s", resp.StatusCode, strings.TrimSpace(string(body))), rerr)
	}
	deleted, err := strconv.ParseInt(resp.Header.Get(storagequota.ArchivedBytesDeletedHeader), 10, 64)
	if err != nil || deleted < 0 {
		return 0, nil
	}
	return deleted, nil
}
