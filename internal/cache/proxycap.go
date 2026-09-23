package cache

import (
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultProxyMaxBytes caps the registry proxy's directory. The proxy serves
// every team and holds only what an upstream registry will hand out again, so
// past the cap the least recently served entries are evicted rather than new
// ones refused: an eviction costs one upstream fetch, a refusal a failed build.
const DefaultProxyMaxBytes int64 = 2 << 30

var (
	proxyMaxBytes = DefaultProxyMaxBytes
	// perf: grown by every store between cap passes, so a store walks the directory only once the
	// running total passes the cap.
	proxyBytes atomic.Int64
	proxyCapMu sync.Mutex
)

type proxyEntry struct {
	registry string
	key      string
	bytes    int64
	lastUse  time.Time
}

// safety: an entry is its .meta and .body together, and its last use is the
// meta's modification time, which a hit refreshes; the expiry pass reads the
// cached-at stamp inside the meta, so refreshing the file never extends a TTL.
func proxyEntries() ([]proxyEntry, int64) {
	var out []proxyEntry
	var total int64
	for name := range defaultRegistries {
		regDir := filepath.Join(proxyDir, name)
		files, err := os.ReadDir(regDir)
		if err != nil {
			continue
		}
		byKey := map[string]*proxyEntry{}
		for _, f := range files {
			info, err := f.Info()
			if err != nil || f.IsDir() {
				continue
			}
			key, kind := strings.TrimSuffix(f.Name(), filepath.Ext(f.Name())), filepath.Ext(f.Name())
			if kind != ".meta" && kind != ".body" {
				continue
			}
			e, ok := byKey[key]
			if !ok {
				e = &proxyEntry{registry: name, key: key}
				byKey[key] = e
			}
			e.bytes += info.Size()
			total += info.Size()
			if kind == ".meta" || e.lastUse.IsZero() {
				e.lastUse = info.ModTime()
			}
		}
		for _, e := range byKey {
			out = append(out, *e)
		}
	}
	return out, total
}

// safety: an entry another request holds is skipped, never waited on, so a slow download cannot stall
// the cap.
func enforceProxyCap() (int, int64) {
	proxyCapMu.Lock()
	defer proxyCapMu.Unlock()
	entries, total := proxyEntries()
	if proxyMaxBytes <= 0 || total <= proxyMaxBytes {
		proxyBytes.Store(total)
		return 0, 0
	}
	slices.SortFunc(entries, func(a, b proxyEntry) int { return a.lastUse.Compare(b.lastUse) })
	evicted, freed := 0, int64(0)
	for _, e := range entries {
		if total-freed <= proxyMaxBytes {
			break
		}
		lock := proxyKeyLock(e.key)
		if !lock.TryLock() {
			continue
		}
		for _, ext := range []string{".meta", ".body"} {
			// #nosec G703 -- a registry from the hard-coded table plus a cache key read off its directory
			if err := os.Remove(filepath.Join(proxyDir, e.registry, e.key+ext)); err != nil && !os.IsNotExist(err) {
				log.Printf("warning: proxy cache: evict %s/%s%s: %v", e.registry, e.key, ext, err)
			}
		}
		lock.Unlock()
		evicted++
		freed += e.bytes
	}
	proxyBytes.Store(total - freed)
	if evicted > 0 {
		log.Printf("proxy cache: evicted %d least recently served entries (%d bytes) to stay under %d bytes",
			evicted, freed, proxyMaxBytes)
	}
	return evicted, freed
}

func noteProxyStored(n int64) {
	if proxyBytes.Add(n) > proxyMaxBytes && proxyMaxBytes > 0 {
		enforceProxyCap()
	}
}

// touchEntry records a read of a proxy entry as its meta file's
// modification time, which the cap's eviction order reads.
func touchEntry(path string) {
	now := time.Now()
	// #nosec G703 -- callers pass a path built from a pattern-validated key
	if err := os.Chtimes(path, now, now); err != nil && !os.IsNotExist(err) {
		log.Printf("warning: record a read of %s: %v", filepath.Base(path), err)
	}
}
