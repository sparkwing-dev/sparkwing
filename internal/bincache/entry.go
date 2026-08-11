package bincache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const pipelineCacheSchema = "v1"

var errCacheAuthorityUnavailable = errors.New("pipeline cache authority unavailable")

var pipelineEntryKeyRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{8}$`)

var removeCacheEntry = os.RemoveAll

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

// PruneOptions bounds one cache-pressure reclamation attempt.
type PruneOptions struct {
	Root         string
	ReclaimBytes int64
	MaxEntries   int
}

// PruneResult reports observed pressure and work completed by Prune.
type PruneResult struct {
	ObservedBytes  int64 `json:"observed_bytes"`
	ReclaimedBytes int64 `json:"reclaimed_bytes"`
	Examined       int   `json:"examined_entries"`
	Reclaimed      int   `json:"reclaimed_entries"`
	Active         int   `json:"active_entries"`
	Busy           int   `json:"busy_entries"`
	GoalSatisfied  bool  `json:"goal_satisfied"`
	Exhausted      bool  `json:"exhausted"`
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
	if write == nil {
		return false, errors.New("pipeline cache materializer is required")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	writer, err := e.openLock("writer", cacheLockExclusive)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, cacheUnlock(writer), writer.Close()) }()
	lease, err := e.openLock("lease", cacheLockExclusive)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, cacheUnlock(lease), lease.Close()) }()
	if _, statErr := os.Stat(e.binaryPath()); statErr == nil {
		return false, nil
	} else if !os.IsNotExist(statErr) {
		return false, statErr
	}
	if err := os.MkdirAll(filepath.Join(e.root, "staging"), 0o700); err != nil {
		return false, err
	}
	stage, err := os.MkdirTemp(filepath.Join(e.root, "staging"), e.key+"-")
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	tempBinary := filepath.Join(stage, filepath.Base(e.binaryPath()))
	if err := write(tempBinary); err != nil {
		return false, err
	}
	info, err := os.Stat(tempBinary)
	if err != nil {
		return false, fmt.Errorf("materialized pipeline binary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("materialized pipeline binary is not a regular file")
	}
	if err := os.MkdirAll(filepath.Dir(e.entryDir()), 0o700); err != nil {
		return false, err
	}
	if err := os.Rename(stage, e.entryDir()); err != nil {
		return false, err
	}
	return true, nil
}

// AcquireOrMaterialize returns a held lease for an existing or newly
// materialized entry.
func (e Entry) AcquireOrMaterialize(context.Context, func(string) error) (*Lease, bool, error) {
	return nil, false, errCacheAuthorityUnavailable
}

// Prune reclaims inactive entries within the requested bounds.
func Prune(ctx context.Context, opts PruneOptions) (result PruneResult, err error) {
	if opts.ReclaimBytes <= 0 {
		return result, errors.New("reclaim bytes must be greater than zero")
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
		result.Busy = 1
		result.Exhausted = true
		return result, nil
	}
	defer func() { err = errors.Join(err, cacheUnlock(coordinator), coordinator.Close()) }()

	candidates, err := cacheCandidates(root)
	if err != nil {
		return result, err
	}
	var pruneErr error
	for _, candidate := range candidates {
		if result.Examined >= opts.MaxEntries || result.ReclaimedBytes >= opts.ReclaimBytes {
			break
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Examined++
		entry := Entry{root: root, key: candidate.key}
		writer, acquired, lockErr := entry.tryLock("writer")
		if lockErr != nil {
			return result, lockErr
		}
		if !acquired {
			result.Busy++
			continue
		}
		size, sizeErr := treeSize(entry.entryDir())
		if sizeErr != nil {
			_ = cacheUnlock(writer)
			_ = writer.Close()
			return result, sizeErr
		}
		result.ObservedBytes += size
		lease, acquired, lockErr := entry.tryLock("lease")
		if lockErr != nil {
			_ = cacheUnlock(writer)
			_ = writer.Close()
			return result, lockErr
		}
		if !acquired {
			result.Active++
			if closeErr := errors.Join(cacheUnlock(writer), writer.Close()); closeErr != nil {
				return result, closeErr
			}
			continue
		}
		removeErr := removeCacheEntry(entry.entryDir())
		closeErr := errors.Join(cacheUnlock(lease), lease.Close(), cacheUnlock(writer), writer.Close())
		if removeErr != nil || closeErr != nil {
			pruneErr = errors.Join(pruneErr, removeErr, closeErr)
			continue
		}
		result.Reclaimed++
		result.ReclaimedBytes += size
	}
	result.GoalSatisfied = result.ReclaimedBytes >= opts.ReclaimBytes
	result.Exhausted = !result.GoalSatisfied && (result.Examined >= opts.MaxEntries || result.Examined == len(candidates))
	return result, pruneErr
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

func cacheCandidates(root string) ([]cacheCandidate, error) {
	entries, err := os.ReadDir(filepath.Join(root, "entries"))
	if os.IsNotExist(err) {
		entries = nil
		err = nil
	}
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]cacheCandidate, len(entries))
	for _, dir := range entries {
		if !dir.IsDir() || !pipelineEntryKeyRE.MatchString(dir.Name()) {
			continue
		}
		info, err := dir.Info()
		if err != nil {
			return nil, err
		}
		byKey[dir.Name()] = cacheCandidate{key: dir.Name(), modTime: info.ModTime().UnixNano()}
	}
	locks, err := os.ReadDir(filepath.Join(root, "locks"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, lock := range locks {
		const suffix = ".writer.lock"
		if lock.IsDir() || !strings.HasSuffix(lock.Name(), suffix) {
			continue
		}
		key := strings.TrimSuffix(lock.Name(), suffix)
		if !pipelineEntryKeyRE.MatchString(key) {
			continue
		}
		if _, exists := byKey[key]; exists {
			continue
		}
		info, err := lock.Info()
		if err != nil {
			return nil, err
		}
		byKey[key] = cacheCandidate{key: key, modTime: info.ModTime().UnixNano()}
	}
	candidates := make([]cacheCandidate, 0, len(byKey))
	for _, candidate := range byKey {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].modTime == candidates[j].modTime {
			return candidates[i].key < candidates[j].key
		}
		return candidates[i].modTime < candidates[j].modTime
	})
	return candidates, nil
}

func treeSize(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(_ string, dir fs.DirEntry, err error) error {
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
