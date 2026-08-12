package bincache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	pipelineCacheSchema      = "v1"
	maxCacheDiscoveryEntries = 4096
	legacyRetirementGrace    = 20 * time.Minute
)

var pipelineEntryKeyRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{8}$`)

var removeCacheEntry = os.RemoveAll
var cacheNow = time.Now

// Entry is the authority to inspect, materialize, and execute one managed
// pipeline-cache entry.
type Entry struct {
	root string
	key  string
}

// Lease keeps an entry live until Release or process replacement.
type Lease struct {
	entry Entry
	file  *os.File
}

// PruneOptions bounds one cache reclamation attempt.
type PruneOptions struct {
	Root         string
	ReclaimBytes int64
	// RemoveBytes is the logical cache-byte goal used by configured ceilings.
	// Reclaim callers use ReclaimBytes and remeasure the filesystem instead.
	RemoveBytes int64
	// ReclaimEntries is an optional entry-count goal used by configured
	// cache ceilings. Reclaim callers normally leave it zero.
	ReclaimEntries int
	MaxEntries     int
}

// PruneResult reports observed pressure and work completed by Prune. A
// successful attempt classifies every examined entry as reclaimed, active, or
// busy. PruneBusy reports coordinator contention without examining an entry.
type PruneResult struct {
	ObservedBytes       int64 `json:"observed_bytes"`
	LogicalRemovedBytes int64 `json:"logical_removed_bytes"`
	// ObservedCapacityGainedBytes is capacity observed becoming available after removal.
	// Admission must remeasure afterward because concurrent filesystem activity
	// prevents attributing the observation to this prune attempt.
	ObservedCapacityGainedBytes int64 `json:"observed_capacity_gained_bytes"`
	ExaminedEntries             int   `json:"examined_entries"`
	ReclaimedEntries            int   `json:"reclaimed_entries"`
	ActiveSkippedEntries        int   `json:"active_skipped_entries"`
	BusySkippedEntries          int   `json:"busy_skipped_entries"`
	PruneBusy                   bool  `json:"prune_busy"`
	GoalSatisfied               bool  `json:"goal_satisfied"`
	// WorkBoundExhausted means discovery or examination reached a caller-set
	// limit; an empty inventory leaves it false.
	WorkBoundExhausted bool `json:"work_bound_exhausted"`
}

// CacheStatus reports the measured managed and legacy pipeline-cache state.
type CacheStatus struct {
	ObservedBytes      int64 `json:"observed_bytes"`
	EntryCount         int   `json:"entry_count"`
	ActiveEntries      int   `json:"active_entries"`
	ActiveBytes        int64 `json:"active_bytes"`
	BusyEntries        int   `json:"busy_entries"`
	LegacyBytes        int64 `json:"legacy_bytes"`
	LegacyEntries      int   `json:"legacy_entries"`
	DiscoveryExhausted bool  `json:"discovery_exhausted"`
}

// PipelineEntry resolves key inside Sparkwing's managed pipeline cache.
func PipelineEntry(key string) (Entry, error) {
	return pipelineEntryAt(filepath.Join(SparkwingHome(), "cache", "pipelines", pipelineCacheSchema), key)
}

func pipelineEntryAt(root, key string) (Entry, error) {
	if root == "" {
		return Entry{}, errors.New("pipeline cache root is required")
	}
	if !pipelineEntryKeyRE.MatchString(key) {
		return Entry{}, fmt.Errorf("pipeline cache key %q must be 8 lowercase hexadecimal characters, a hyphen, and 8 lowercase hexadecimal characters", key)
	}
	return Entry{root: root, key: key}, nil
}

// Path returns the executable protected by the lease.
func (l *Lease) Path() string {
	if l == nil {
		return ""
	}
	return l.entry.binaryPath()
}

// Release relinquishes the entry lease.
func (l *Lease) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := errors.Join(cacheUnlock(l.file), l.file.Close())
	l.file = nil
	return err
}

// ExecReplace replaces the current process with the leased entry.
func (l *Lease) ExecReplace(args []string, dir string, env []string) error {
	if l == nil || l.file == nil {
		return errors.New("pipeline cache lease is not held")
	}
	restore, err := cacheRetainAcrossExec(l.file)
	if err != nil {
		return err
	}
	return errors.Join(ExecReplace(l.Path(), args, dir, env), restore())
}

// Acquire obtains a live-entry lease if the entry exists.
func (e Entry) Acquire(ctx context.Context) (*Lease, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	lease, err := e.openLock("lease", cacheLockShared)
	if err != nil {
		return nil, false, err
	}
	if _, err := os.Stat(e.binaryPath()); err != nil {
		closeErr := errors.Join(cacheUnlock(lease), lease.Close())
		if os.IsNotExist(err) {
			return nil, false, closeErr
		}
		return nil, false, errors.Join(err, closeErr)
	}
	return &Lease{entry: e, file: lease}, true, nil
}

// Materialize publishes the entry through a private staging path.
func (e Entry) Materialize(ctx context.Context, write func(string) error) (published bool, err error) {
	lease, published, err := e.AcquireOrMaterialize(ctx, write)
	if lease != nil {
		err = errors.Join(err, lease.Release())
	}
	return published, err
}

// AcquireOrMaterialize returns a held lease for an existing or newly
// materialized entry.
func (e Entry) AcquireOrMaterialize(ctx context.Context, write func(string) error) (_ *Lease, published bool, err error) {
	if write == nil {
		return nil, false, errors.New("pipeline cache materializer is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if lease, found, acquireErr := e.Acquire(ctx); acquireErr != nil {
		return nil, false, acquireErr
	} else if found {
		return lease, false, nil
	}
	writer, err := e.openLock("writer", cacheLockExclusive)
	if err != nil {
		return nil, false, err
	}
	writerOpen := true
	defer func() {
		if writerOpen {
			err = errors.Join(err, cacheUnlock(writer), writer.Close())
		}
	}()
	if lease, found, acquireErr := e.Acquire(ctx); acquireErr != nil {
		return nil, false, acquireErr
	} else if found {
		return lease, false, nil
	}
	lease, err := e.openLock("lease", cacheLockExclusive)
	if err != nil {
		return nil, false, err
	}
	leaseReturned := false
	defer func() {
		if !leaseReturned {
			err = errors.Join(err, cacheUnlock(lease), lease.Close())
		}
	}()
	if _, statErr := os.Stat(e.binaryPath()); statErr == nil {
		if err := cacheLeaseReady(lease); err != nil {
			return nil, false, err
		}
		leaseReturned = true
		return &Lease{entry: e, file: lease}, false, nil
	} else if !os.IsNotExist(statErr) {
		return nil, false, statErr
	}
	queueSequence, err := enqueueCacheEntry(ctx, e.root, e.key)
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Join(e.root, "staging"), 0o700); err != nil {
		return nil, false, err
	}
	stage, err := os.MkdirTemp(filepath.Join(e.root, "staging"), e.key+"-")
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	tempBinary := filepath.Join(stage, filepath.Base(e.binaryPath()))
	if err := write(tempBinary); err != nil {
		return nil, false, err
	}
	info, err := os.Stat(tempBinary)
	if err != nil {
		return nil, false, fmt.Errorf("materialized pipeline binary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("materialized pipeline binary is not a regular file")
	}
	if err := os.MkdirAll(filepath.Dir(e.entryDir()), 0o700); err != nil {
		return nil, false, err
	}
	if err := os.Rename(stage, e.entryDir()); err != nil {
		return nil, false, err
	}
	usedAt := cacheNow()
	_ = os.Chtimes(e.entryDir(), usedAt, usedAt)
	_ = markCacheQueueRecordCurrent(e.root, queueSequence, usedAt)
	if err := cacheLeaseReady(lease); err != nil {
		return nil, false, err
	}
	if err := errors.Join(cacheUnlock(writer), writer.Close()); err != nil {
		return nil, false, err
	}
	writerOpen = false
	leaseReturned = true
	_, _ = pruneToLimitsAtRoot(ctx, e.root, ConfiguredMaxBytes(), ConfiguredMaxEntries(), false)
	return &Lease{entry: e, file: lease}, true, nil
}

// Prune reclaims inactive entries within the requested bounds.
func Prune(ctx context.Context, opts PruneOptions) (result PruneResult, err error) {
	if opts.ReclaimBytes <= 0 && opts.RemoveBytes <= 0 && opts.ReclaimEntries <= 0 {
		return result, errors.New("a positive reclaim byte or entry goal is required")
	}
	if opts.MaxEntries <= 0 {
		return result, errors.New("max entries must be greater than zero")
	}
	root := opts.Root
	if root == "" {
		root = filepath.Join(SparkwingHome(), "cache", "pipelines", pipelineCacheSchema)
	}
	coordinator, acquired, err := openCacheLock(root, "prune", cacheLockExclusiveNonblock)
	if err != nil {
		return result, err
	}
	if !acquired {
		result.PruneBusy = true
		return result, nil
	}
	defer func() { err = errors.Join(err, cacheUnlock(coordinator), coordinator.Close()) }()

	discoveryLimit := boundedCacheDiscoveryLimit(opts.MaxEntries)
	candidates, managedExhausted, err := cacheQueueBatch(ctx, root, discoveryLimit)
	if err != nil {
		return result, err
	}
	var pruneErr error
	for _, candidate := range candidates {
		if result.ExaminedEntries >= opts.MaxEntries || pruneGoalSatisfied(result, opts) {
			break
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if candidate.missing {
			if err := consumeCacheQueueRecord(ctx, root, candidate.sequence); err != nil {
				return result, err
			}
			continue
		}
		entry := Entry{root: root, key: candidate.key}
		writer, acquired, lockErr := entry.tryLock("writer")
		if lockErr != nil {
			return result, lockErr
		}
		if !acquired {
			result.ExaminedEntries++
			result.BusySkippedEntries++
			if err := requeueCacheCandidate(ctx, root, candidate); err != nil {
				return result, err
			}
			continue
		}
		size, sizeErr := treeSizeContext(ctx, entry.entryDir())
		if os.IsNotExist(sizeErr) {
			if closeErr := errors.Join(cacheUnlock(writer), writer.Close()); closeErr != nil {
				return result, closeErr
			}
			if err := consumeCacheQueueRecord(ctx, root, candidate.sequence); err != nil {
				return result, err
			}
			continue
		}
		if sizeErr != nil {
			_ = cacheUnlock(writer)
			_ = writer.Close()
			return result, sizeErr
		}
		result.ObservedBytes += size
		if info, statErr := os.Stat(entry.entryDir()); statErr != nil {
			_ = cacheUnlock(writer)
			_ = writer.Close()
			return result, statErr
		} else if info.ModTime().UnixNano() > candidate.modTime {
			result.ExaminedEntries++
			result.BusySkippedEntries++
			if closeErr := errors.Join(cacheUnlock(writer), writer.Close()); closeErr != nil {
				return result, closeErr
			}
			if err := requeueCacheCandidate(ctx, root, candidate); err != nil {
				return result, err
			}
			continue
		}
		lease, acquired, lockErr := entry.tryLock("lease")
		if lockErr != nil {
			_ = cacheUnlock(writer)
			_ = writer.Close()
			return result, lockErr
		}
		if !acquired {
			result.ExaminedEntries++
			result.ActiveSkippedEntries++
			if closeErr := errors.Join(cacheUnlock(writer), writer.Close()); closeErr != nil {
				return result, closeErr
			}
			if err := requeueCacheCandidate(ctx, root, candidate); err != nil {
				return result, err
			}
			continue
		}
		result.ExaminedEntries++
		reclaimedBytes, removeErr := removeCacheEntryWithCapacity(ctx, entry.entryDir())
		closeErr := errors.Join(cacheUnlock(lease), lease.Close(), cacheUnlock(writer), writer.Close())
		if removeErr != nil || closeErr != nil {
			pruneErr = errors.Join(pruneErr, removeErr, closeErr)
			if queueErr := requeueCacheCandidate(ctx, root, candidate); queueErr != nil {
				return result, errors.Join(pruneErr, queueErr)
			}
			continue
		}
		result.ReclaimedEntries++
		result.LogicalRemovedBytes += size
		result.ObservedCapacityGainedBytes += reclaimedBytes
		if err := consumeCacheQueueRecord(ctx, root, candidate.sequence); err != nil {
			return result, err
		}
	}
	remaining := discoveryLimit - result.ExaminedEntries
	var legacy []legacyCacheCandidate
	legacyExhausted := false
	if remaining > 0 && !pruneGoalSatisfied(result, opts) {
		legacy, legacyExhausted, err = legacyCacheCandidates(ctx, root, remaining)
		if err != nil {
			return result, err
		}
	}
	for _, candidate := range legacy {
		if result.ExaminedEntries >= opts.MaxEntries || pruneGoalSatisfied(result, opts) {
			break
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		size, sizeErr := treeSizeContext(ctx, candidate.path)
		if os.IsNotExist(sizeErr) {
			continue
		}
		if sizeErr != nil {
			return result, sizeErr
		}
		result.ObservedBytes += size
		if !candidate.retired {
			retiredRoot := filepath.Join(root, "legacy-retired")
			if mkdirErr := os.MkdirAll(retiredRoot, 0o700); mkdirErr != nil {
				return result, mkdirErr
			}
			retiredPath := filepath.Join(retiredRoot, filepath.Base(candidate.path))
			if renameErr := os.Rename(candidate.path, retiredPath); renameErr != nil {
				if os.IsNotExist(renameErr) {
					continue
				}
				pruneErr = errors.Join(pruneErr, renameErr)
				continue
			}
			if timeErr := os.Chtimes(retiredPath, cacheNow(), cacheNow()); timeErr != nil {
				pruneErr = errors.Join(pruneErr, timeErr)
			}
			result.ExaminedEntries++
			result.ActiveSkippedEntries++
			continue
		}
		if cacheNow().Sub(time.Unix(0, candidate.modTime)) < legacyRetirementGrace {
			result.ExaminedEntries++
			result.ActiveSkippedEntries++
			continue
		}
		result.ExaminedEntries++
		reclaimedBytes, removeErr := removeCacheEntryWithCapacity(ctx, candidate.path)
		if removeErr != nil {
			pruneErr = errors.Join(pruneErr, removeErr)
			continue
		}
		result.ReclaimedEntries++
		result.LogicalRemovedBytes += size
		result.ObservedCapacityGainedBytes += reclaimedBytes
	}
	result.GoalSatisfied = pruneGoalSatisfied(result, opts)
	result.WorkBoundExhausted = !result.GoalSatisfied &&
		(legacyExhausted || managedExhausted || result.ExaminedEntries >= opts.MaxEntries)
	return result, pruneErr
}

func requeueCacheCandidate(ctx context.Context, root string, candidate queuedCacheCandidate) error {
	if _, err := enqueueCacheEntry(ctx, root, candidate.key); err != nil {
		return err
	}
	return consumeCacheQueueRecord(ctx, root, candidate.sequence)
}

func pruneGoalSatisfied(result PruneResult, opts PruneOptions) bool {
	bytesSatisfied := opts.ReclaimBytes <= 0 || result.ObservedCapacityGainedBytes >= opts.ReclaimBytes
	removedSatisfied := opts.RemoveBytes <= 0 || result.LogicalRemovedBytes >= opts.RemoveBytes
	entriesSatisfied := opts.ReclaimEntries <= 0 || result.ReclaimedEntries >= opts.ReclaimEntries
	return bytesSatisfied && removedSatisfied && entriesSatisfied
}

// Status measures the pipeline cache without deleting entries.
func Status(ctx context.Context, root string) (status CacheStatus, err error) {
	if root == "" {
		root = filepath.Join(SparkwingHome(), "cache", "pipelines", pipelineCacheSchema)
	}
	candidates, exhausted, err := cacheCandidatesBounded(ctx, root, maxCacheDiscoveryEntries)
	if err != nil {
		return status, err
	}
	status.DiscoveryExhausted = exhausted
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return status, err
		}
		entry := Entry{root: root, key: candidate.key}
		writer, acquired, err := entry.tryLock("writer")
		if err != nil {
			return status, err
		}
		if !acquired {
			status.BusyEntries++
			continue
		}
		size, sizeErr := treeSizeContext(ctx, entry.entryDir())
		if os.IsNotExist(sizeErr) {
			if closeErr := errors.Join(cacheUnlock(writer), writer.Close()); closeErr != nil {
				return status, closeErr
			}
			continue
		}
		if sizeErr != nil {
			_ = cacheUnlock(writer)
			_ = writer.Close()
			return status, sizeErr
		}
		status.EntryCount++
		status.ObservedBytes += size
		lease, acquired, lockErr := entry.tryLock("lease")
		if lockErr != nil {
			_ = cacheUnlock(writer)
			_ = writer.Close()
			return status, lockErr
		}
		if !acquired {
			status.ActiveEntries++
			status.ActiveBytes += size
		} else if closeErr := errors.Join(cacheUnlock(lease), lease.Close()); closeErr != nil {
			_ = cacheUnlock(writer)
			_ = writer.Close()
			return status, closeErr
		}
		if closeErr := errors.Join(cacheUnlock(writer), writer.Close()); closeErr != nil {
			return status, closeErr
		}
	}
	if filepath.Base(root) != pipelineCacheSchema {
		return status, nil
	}
	legacy, legacyExhausted, err := legacyCacheCandidates(ctx, root, maxCacheDiscoveryEntries)
	if err != nil {
		return status, err
	}
	status.DiscoveryExhausted = status.DiscoveryExhausted || legacyExhausted
	for _, candidate := range legacy {
		size, err := treeSizeContext(ctx, candidate.path)
		if err != nil {
			return status, err
		}
		status.LegacyEntries++
		status.LegacyBytes += size
	}
	return status, nil
}

func (e Entry) binaryPath() string {
	name := "pipelines"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(e.root, "entries", e.key, name)
}

func (e Entry) entryDir() string {
	return filepath.Join(e.root, "entries", e.key)
}

func (e Entry) lockPath(kind string) string {
	if kind == "writer" {
		return filepath.Join(e.root, "writers", e.key+".lock")
	}
	return filepath.Join(e.root, "locks", e.key+"."+kind+".lock")
}

func (e Entry) openLock(kind string, mode cacheLockMode) (*os.File, error) {
	lock, acquired, err := openCacheLockPath(e.lockPath(kind), mode)
	if err != nil {
		return nil, err
	}
	if !acquired {
		_ = lock.Close()
		return nil, errors.New("pipeline cache lock is busy")
	}
	return lock, nil
}

func (e Entry) tryLock(kind string) (*os.File, bool, error) {
	return openCacheLockPath(e.lockPath(kind), cacheLockExclusiveNonblock)
}

func openCacheLock(root, name string, mode cacheLockMode) (*os.File, bool, error) {
	return openCacheLockPath(filepath.Join(root, "locks", name+".lock"), mode)
}

func openCacheLockPath(path string, mode cacheLockMode) (*os.File, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	acquired, err := cacheLock(file, mode)
	if err != nil {
		_ = file.Close()
		return nil, false, err
	}
	if !acquired {
		_ = file.Close()
		return nil, false, nil
	}
	return file, true, nil
}

type cacheCandidate struct {
	key     string
	modTime int64
}

func cacheCandidates(ctx context.Context, root string, limit int) ([]cacheCandidate, error) {
	candidates, _, err := cacheCandidatesBounded(ctx, root, limit)
	return candidates, err
}

func cacheCandidatesBounded(ctx context.Context, root string, limit int) ([]cacheCandidate, bool, error) {
	entries, exhausted, err := readDirBounded(ctx, filepath.Join(root, "entries"), limit)
	if err != nil {
		return nil, false, err
	}
	candidates := make([]cacheCandidate, 0, len(entries))
	for _, dir := range entries {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if !dir.IsDir() || !pipelineEntryKeyRE.MatchString(dir.Name()) {
			continue
		}
		info, err := dir.Info()
		if err != nil {
			return nil, false, err
		}
		candidates = append(candidates, cacheCandidate{key: dir.Name(), modTime: info.ModTime().UnixNano()})
	}
	remaining := limit - len(candidates)
	if remaining > 0 && !exhausted {
		locks, locksExhausted, err := readDirBounded(ctx, filepath.Join(root, "writers"), remaining)
		if err != nil {
			return nil, false, err
		}
		exhausted = locksExhausted
		for _, lock := range locks {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			const suffix = ".lock"
			if lock.IsDir() || !strings.HasSuffix(lock.Name(), suffix) {
				continue
			}
			key := strings.TrimSuffix(lock.Name(), suffix)
			if !pipelineEntryKeyRE.MatchString(key) || candidateKeyExists(candidates, key) {
				continue
			}
			entry := Entry{root: root, key: key}
			writer, acquired, err := entry.tryLock("writer")
			if err != nil {
				return nil, false, err
			}
			if acquired {
				if closeErr := errors.Join(cacheUnlock(writer), writer.Close()); closeErr != nil {
					return nil, false, closeErr
				}
				continue
			}
			info, err := lock.Info()
			if err != nil {
				return nil, false, err
			}
			candidates = append(candidates, cacheCandidate{key: key, modTime: info.ModTime().UnixNano()})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].modTime == candidates[j].modTime {
			return candidates[i].key < candidates[j].key
		}
		return candidates[i].modTime < candidates[j].modTime
	})
	return candidates, exhausted, nil
}

func candidateKeyExists(candidates []cacheCandidate, key string) bool {
	for _, candidate := range candidates {
		if candidate.key == key {
			return true
		}
	}
	return false
}

func boundedCacheDiscoveryLimit(limit int) int {
	if limit > maxCacheDiscoveryEntries {
		return maxCacheDiscoveryEntries
	}
	return limit
}

func treeSize(root string) (int64, error) {
	return treeSizeContext(context.Background(), root)
}

func treeSizeContext(ctx context.Context, root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(_ string, dir fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		if dir.IsDir() {
			return nil
		}
		info, err := dir.Info()
		if err != nil {
			return err
		}
		size += info.Size()
		return nil
	})
	return size, err
}

type legacyCacheCandidate struct {
	path    string
	modTime int64
	retired bool
}

func legacyCacheCandidates(ctx context.Context, root string, limit int) ([]legacyCacheCandidate, bool, error) {
	if filepath.Base(root) != pipelineCacheSchema {
		return nil, false, nil
	}
	retired, exhausted, err := readDirBounded(ctx, filepath.Join(root, "legacy-retired"), limit)
	if err != nil {
		return nil, false, err
	}
	candidates := make([]legacyCacheCandidate, 0, limit)
	for _, entry := range retired {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if !entry.IsDir() || !pipelineEntryKeyRE.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, false, err
		}
		candidates = append(candidates, legacyCacheCandidate{path: filepath.Join(root, "legacy-retired", entry.Name()), modTime: info.ModTime().UnixNano(), retired: true})
	}
	remaining := limit - len(candidates)
	if remaining <= 0 || exhausted {
		sortLegacyCandidates(candidates)
		return candidates, true, nil
	}
	entries, liveExhausted, err := readDirBounded(ctx, filepath.Dir(root), remaining+1)
	if err != nil {
		return nil, false, err
	}
	exhausted = liveExhausted
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if !entry.IsDir() || !pipelineEntryKeyRE.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, false, err
		}
		candidates = append(candidates, legacyCacheCandidate{path: filepath.Join(filepath.Dir(root), entry.Name()), modTime: info.ModTime().UnixNano()})
		if len(candidates) == limit {
			exhausted = true
			break
		}
	}
	sortLegacyCandidates(candidates)
	return candidates, exhausted, nil
}

func sortLegacyCandidates(candidates []legacyCacheCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].retired != candidates[j].retired {
			return candidates[i].retired
		}
		if candidates[i].modTime == candidates[j].modTime {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].modTime < candidates[j].modTime
	})
}

func readDirBounded(ctx context.Context, path string, limit int) ([]os.DirEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if limit <= 0 {
		return nil, false, nil
	}
	dir, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	entries, readErr := dir.ReadDir(limit + 1)
	closeErr := dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, false, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	exhausted := len(entries) > limit
	if exhausted {
		entries = entries[:limit]
	}
	return entries, exhausted, nil
}
