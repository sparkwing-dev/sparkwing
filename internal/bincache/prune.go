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

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

const (
	DefaultMaxCacheBytes   int64 = 2 << 30
	DefaultMaxCacheEntries       = 20
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
	limits := Limits{MaxBytes: DefaultMaxCacheBytes, MaxEntries: DefaultMaxCacheEntries}
	var raw cacheConfigFile
	path, _, err := userconfig.ReadDefault(userconfig.Cache, &raw)
	if err != nil {
		return limits, err
	}
	if v := strings.TrimSpace(raw.MaxBytes); v != "" {
		n, err := ParseBytes(v)
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
