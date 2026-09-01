package bincache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultMaxCacheBytes   int64 = 2 << 30
	DefaultMaxCacheEntries       = 20

	MaxCacheBytesEnv   = "SPARKWING_CACHE_MAX_BYTES"
	MaxCacheEntriesEnv = "SPARKWING_CACHE_MAX_ENTRIES"
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
			entry.Owners = Owners(de.Name())
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

func PruneToConfiguredLimits(ctx context.Context) (PruneResult, error) {
	return PruneToLimits(ctx, ConfiguredMaxBytes(), ConfiguredMaxEntries(), false)
}

func PruneToLimits(ctx context.Context, maxBytes int64, maxEntries int, removeAll bool) (PruneResult, error) {
	return pruneToLimitsAtRoot(ctx, "", maxBytes, maxEntries, removeAll)
}

func pruneToLimitsAtRoot(ctx context.Context, root string, maxBytes int64, maxEntries int, removeAll bool) (PruneResult, error) {
	status, err := statusForLimits(ctx, root)
	if err != nil {
		return PruneResult{}, err
	}
	total := status.ObservedBytes + status.LegacyBytes
	count := status.EntryCount + status.LegacyEntries
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
	if bytesGoal == 0 && entriesGoal == 0 && !status.DiscoveryExhausted {
		return PruneResult{GoalSatisfied: true}, nil
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

func ConfiguredMaxBytes() int64 {
	raw := os.Getenv(MaxCacheBytesEnv)
	if raw == "" {
		return DefaultMaxCacheBytes
	}
	n, err := ParseBytes(raw)
	if err != nil {
		return DefaultMaxCacheBytes
	}
	return n
}

func ConfiguredMaxEntries() int {
	raw := os.Getenv(MaxCacheEntriesEnv)
	if raw == "" {
		return DefaultMaxCacheEntries
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return DefaultMaxCacheEntries
	}
	return n
}

func ParseBytes(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	units := []struct {
		suffix string
		mult   int64
	}{
		{"KIB", 1 << 10},
		{"MIB", 1 << 20},
		{"GIB", 1 << 30},
		{"TIB", 1 << 40},
		{"KB", 1000},
		{"MB", 1000 * 1000},
		{"GB", 1000 * 1000 * 1000},
		{"TB", 1000 * 1000 * 1000 * 1000},
		{"B", 1},
	}
	upper := strings.ToUpper(s)
	for _, u := range units {
		if !strings.HasSuffix(upper, u.suffix) {
			continue
		}
		num := strings.TrimSpace(upper[:len(upper)-len(u.suffix)])
		n, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return 0, fmt.Errorf("parse size %q: %w", raw, err)
		}
		if n < 0 {
			return 0, fmt.Errorf("parse size %q: negative", raw)
		}
		return int64(n * float64(u.mult)), nil
	}
	n, err := strconv.ParseInt(upper, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse size %q: %w", raw, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("parse size %q: negative", raw)
	}
	return n, nil
}
