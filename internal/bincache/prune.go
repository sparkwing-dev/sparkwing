package bincache

import (
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

// Cache sizing. A compiled pipeline binary routinely exceeds 90 MB, and
// nothing evicted them before this, so an unattended cache grew without
// bound -- tens of gigabytes for binaries whose source tree had since
// been deleted.
//
// The byte ceiling is the real guard and the entry count is a secondary
// backstop. A count alone would mean wildly different disk footprints
// between a large project and a small one, since only the former's
// binaries are enormous.
const (
	DefaultMaxCacheBytes   int64 = 2 << 30 // 2 GiB
	DefaultMaxCacheEntries       = 20

	// MaxCacheBytesEnv and MaxCacheEntriesEnv override the defaults.
	// Both accept 0 for "no limit"; the byte form also accepts a size
	// suffix, as in 512MB or 4GiB.
	MaxCacheBytesEnv   = "SPARKWING_CACHE_MAX_BYTES"
	MaxCacheEntriesEnv = "SPARKWING_CACHE_MAX_ENTRIES"
)

// pruneGrace protects an entry that was touched very recently. A run
// stats its binary and then execs it; deleting in that window would
// turn a cache hit into a spurious failure. Callers additionally
// tolerate the exec losing that race (see the ErrNotExist retry in the
// compile path), so this is defense in depth rather than the whole
// answer.
const pruneGrace = 5 * time.Minute

// CacheRoot is the directory holding compiled pipeline binaries, one
// subdirectory per cache key.
func CacheRoot() string {
	return filepath.Join(SparkwingHome(), "cache", "pipelines")
}

// CacheEntry is one cached pipeline binary.
type CacheEntry struct {
	Key      string    // cache key, the subdirectory name
	Dir      string    // absolute path of the entry directory
	Bytes    int64     // size of the binary, 0 for an orphaned directory
	LastUsed time.Time // refreshed on every cache hit; zero when orphaned
	Owners   []Owner   // checkouts known to have used it, most recent first
}

// PruneResult reports what a prune did.
type PruneResult struct {
	Removed    int   // entries deleted
	Freed      int64 // bytes their binaries occupied
	Kept       int   // entries retained
	KeptBytes  int64
	Skipped    int // entries that could not be deleted, e.g. a running .exe
	Considered int
}

// ScanCache lists the cache entries, newest use first. A missing cache
// root is not an error; it scans as empty.
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
		entry := CacheEntry{Key: de.Name(), Dir: filepath.Join(root, de.Name())}
		// An entry directory with no binary is debris from an
		// interrupted compile; it sorts oldest and gets swept first.
		if fi, err := os.Stat(CachedBinaryPath(de.Name())); err == nil {
			entry.Bytes = fi.Size()
			entry.LastUsed = fi.ModTime()
			entry.Owners = Owners(de.Name())
		}
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].LastUsed.After(entries[j].LastUsed)
	})
	return entries, nil
}

// Touch records that an entry was just used. The binary's modification
// time doubles as its last-used stamp: nothing else writes to a cached
// binary after it is built, so without this the timestamp would say
// when it was compiled and a least-recently-used policy would evict
// binaries that are in daily use.
//
// Failure is not worth surfacing -- a read-only or exotic filesystem
// costs accuracy in eviction order, nothing more.
func Touch(binPath string) {
	now := time.Now()
	_ = os.Chtimes(binPath, now, now)
}

// Prune deletes least-recently-used entries until the cache fits within
// both ceilings. A ceiling of 0 disables that dimension. Entries used
// within pruneGrace are never evicted.
//
// Deletion failures are counted and skipped rather than returned:
// Windows refuses to unlink a running executable, and a concurrent
// prune may have removed the entry already. Either way the next prune
// picks it up.
func Prune(maxBytes int64, maxEntries int) (PruneResult, error) {
	entries, err := ScanCache()
	if err != nil {
		return PruneResult{}, err
	}

	result := PruneResult{Considered: len(entries)}
	var total int64
	for _, e := range entries {
		total += e.Bytes
	}
	count := len(entries)

	// entries is newest-first; walk backwards to evict the coldest.
	cutoff := time.Now().Add(-pruneGrace)
	for i := len(entries) - 1; i >= 0; i-- {
		overBytes := maxBytes > 0 && total > maxBytes
		overCount := maxEntries > 0 && count > maxEntries
		if !overBytes && !overCount {
			break
		}
		e := entries[i]
		if e.LastUsed.After(cutoff) {
			// Everything above this point is even newer, so there is
			// nothing colder left that is safe to take.
			break
		}
		if err := os.RemoveAll(e.Dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			result.Skipped++
			continue
		}
		result.Removed++
		result.Freed += e.Bytes
		total -= e.Bytes
		count--
	}

	result.Kept = count
	result.KeptBytes = total
	return result, nil
}

// PruneToConfiguredLimits applies the ceilings from the environment.
func PruneToConfiguredLimits() (PruneResult, error) {
	return Prune(ConfiguredMaxBytes(), ConfiguredMaxEntries())
}

// ConfiguredMaxBytes resolves the byte ceiling, falling back to the
// default when unset or unparseable.
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

// ConfiguredMaxEntries resolves the entry ceiling, falling back to the
// default when unset or unparseable.
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

// ParseBytes reads a byte count, with or without a size suffix:
// "2147483648", "2GiB", "512MB". Decimal suffixes are powers of 1000
// and binary suffixes powers of 1024, matching the units they name.
func ParseBytes(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	units := []struct {
		suffix string
		mult   int64
	}{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30}, {"TIB", 1 << 40},
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000}, {"TB", 1000 * 1000 * 1000 * 1000},
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
