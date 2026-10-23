package bincache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

const (
	defaultMaxCacheBytes   int64 = limits.LocalBinaryCacheBytes
	defaultMaxCacheEntries       = limits.LocalBinaryCacheEntries
)

var (
	statusForLimits = Status
	pruneForLimits  = Prune
)

func CacheRoot() string {
	return filepath.Join(SparkwingHome(), "cache", "pipelines", pipelineCacheSchema, "entries")
}

type CacheEntry struct {
	Key      string
	Dir      string
	Bytes    int64
	LastUsed time.Time
	Owners   []Owner
}

func ScanCache() ([]CacheEntry, error) {
	root := CacheRoot()
	dirents, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	entries := make([]CacheEntry, 0, len(dirents))
	for _, de := range dirents {
		if !de.IsDir() {
			continue
		}
		managed, entryErr := PipelineEntry(de.Name())
		if entryErr != nil {
			continue
		}
		entry := CacheEntry{Key: de.Name(), Dir: filepath.Join(root, de.Name())}
		if fi, err := os.Stat(managed.binaryPath()); err == nil {
			entry.Bytes = fi.Size()
			if dirInfo, infoErr := de.Info(); infoErr == nil {
				entry.LastUsed = dirInfo.ModTime()
			}
			entry.Owners = owners(de.Name())
		}
		entries = append(entries, entry)
	}
	sortCacheEntries(entries)
	return entries, nil
}

func sortCacheEntries(entries []CacheEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].LastUsed.After(entries[j].LastUsed)
	})
}

func PruneToLimits(ctx context.Context, maxBytes int64, maxEntries int, removeAll bool) (PruneResult, error) {
	return pruneToLimitsAtRoot(ctx, "", maxBytes, maxEntries, removeAll)
}

func pruneToLimitsAtRoot(ctx context.Context, root string, maxBytes int64, maxEntries int, removeAll bool) (PruneResult, error) {
	status, err := statusForLimits(ctx, root)
	if err != nil {
		return PruneResult{}, err
	}
	total := status.ObservedBytes
	count := status.EntryCount
	bytesGoal := total - maxBytes
	if maxBytes <= 0 || bytesGoal < 0 {
		bytesGoal = 0
	}
	entriesGoal := count - maxEntries
	if maxEntries <= 0 || entriesGoal < 0 {
		entriesGoal = 0
	}
	if removeAll {
		bytesGoal = 0
		entriesGoal = count
	}
	if bytesGoal == 0 && entriesGoal == 0 {
		return PruneResult{GoalSatisfied: !status.DiscoveryExhausted, WorkBoundExhausted: status.DiscoveryExhausted}, nil
	}
	result, err := pruneForLimits(ctx, PruneOptions{
		Root:           root,
		RemoveBytes:    bytesGoal,
		ReclaimEntries: entriesGoal,
		MaxEntries:     count,
	})
	if status.DiscoveryExhausted {
		result.GoalSatisfied = false
		result.WorkBoundExhausted = true
	}
	return result, err
}

// Limits are the pipeline binary cache ceilings. Zero disables one.
type Limits struct {
	MaxBytes   int64
	MaxEntries int
}

type cacheConfigFile struct {
	MaxBytes   string `yaml:"max_bytes"`
	MaxEntries *int   `yaml:"max_entries"`
}

// ConfiguredLimits reads cache.max_bytes and cache.max_entries from
// config.yaml, defaulting each one the file leaves unset. A value that does
// not parse is an error naming the file and key.
func ConfiguredLimits() (Limits, error) {
	limits := Limits{MaxBytes: defaultMaxCacheBytes, MaxEntries: defaultMaxCacheEntries}
	var raw cacheConfigFile
	path, _, err := userconfig.ReadDefault(userconfig.Cache, &raw)
	if err != nil {
		return limits, err
	}
	if v := strings.TrimSpace(raw.MaxBytes); v != "" {
		n, err := userconfig.ParseByteSize(v)
		if err != nil {
			return limits, fmt.Errorf("%s cache.max_bytes: %w", path, err)
		}
		limits.MaxBytes = n
	}
	if raw.MaxEntries != nil {
		if *raw.MaxEntries < 0 {
			return limits, fmt.Errorf("%s cache.max_entries: %d is negative; 0 disables the entry ceiling", path, *raw.MaxEntries)
		}
		limits.MaxEntries = *raw.MaxEntries
	}
	return limits, nil
}
