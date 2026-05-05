package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Registry defines an upstream package registry that the proxy can cache.
type Registry struct {
	Name        string
	Upstream    string // base URL (no trailing slash)
	RewriteBody bool   // whether response bodies need URL rewriting
}

var defaultRegistries = map[string]Registry{
	"npm":          {Name: "npm", Upstream: "https://registry.npmjs.org", RewriteBody: true},
	"pypi":         {Name: "pypi", Upstream: "https://pypi.org", RewriteBody: true},
	"pythonhosted": {Name: "pythonhosted", Upstream: "https://files.pythonhosted.org", RewriteBody: false},
	"rubygems":     {Name: "rubygems", Upstream: "https://rubygems.org", RewriteBody: false},
	"golang":       {Name: "golang", Upstream: "https://proxy.golang.org", RewriteBody: false},
	"alpine":       {Name: "alpine", Upstream: "https://dl-cdn.alpinelinux.org", RewriteBody: false},
}

// proxyMeta is stored alongside the cached body on disk.
type proxyMeta struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	CachedAt    int64  `json:"cached_at"`
	Size        int64  `json:"size"`
	Immutable   bool   `json:"immutable"`
	StatusCode  int    `json:"status_code"`
}

var (
	proxyDir      = "/data/proxy"
	proxyCacheTTL = 10 * time.Minute
	proxyMaxAge   = 7 * 24 * time.Hour // cleanup threshold for immutable entries
	proxyClient   = &http.Client{Timeout: 60 * time.Second}

	// Per-key RWMutex: concurrent reads for cache hits, exclusive write for fetches
	proxyKeyLocks   = map[string]*sync.RWMutex{}
	proxyKeyLocksMu sync.Mutex
)

func proxyKeyLock(key string) *sync.RWMutex {
	proxyKeyLocksMu.Lock()
	defer proxyKeyLocksMu.Unlock()
	if _, ok := proxyKeyLocks[key]; !ok {
		proxyKeyLocks[key] = &sync.RWMutex{}
	}
	return proxyKeyLocks[key]
}

func initProxy() {
	if d := os.Getenv("PROXY_CACHE_DIR"); d != "" {
		proxyDir = d
	}
	if s := os.Getenv("PROXY_CACHE_TTL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			proxyCacheTTL = d
		}
	}
	if s := os.Getenv("PROXY_MAX_AGE"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			proxyMaxAge = d
		}
	}
	for name := range defaultRegistries {
		os.MkdirAll(filepath.Join(proxyDir, name), 0o755)
	}
}

// handleProxy routes /proxy/{registry}/{path...} requests.
func handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET or HEAD only", http.StatusMethodNotAllowed)
		return
	}

	// Parse /proxy/{registry}/{path...}
	trimmed := strings.TrimPrefix(r.URL.Path, "/proxy/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "usage: /proxy/{registry}/{path...}", http.StatusBadRequest)
		return
	}

	registryName := parts[0]
	remotePath := ""
	if len(parts) == 2 {
		remotePath = parts[1]
	}

	reg, ok := defaultRegistries[registryName]
	if !ok {
		http.Error(w, fmt.Sprintf("unknown registry %q — supported: %s", registryName, registryList()), http.StatusBadRequest)
		return
	}

	// Sanitize: reject path traversal
	if strings.Contains(remotePath, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	key := proxyCacheKey(registryName, remotePath)
	lock := proxyKeyLock(key)

	// Try read lock first (concurrent cache hits)
	lock.RLock()
	if served := proxyServeFromCache(w, r, registryName, key); served {
		lock.RUnlock()
		if proxyCacheHitsCounter != nil {
			proxyCacheHitsCounter.Add(r.Context(), 1,
				metric.WithAttributes(attribute.String("registry", registryName)))
		}
		return
	}
	lock.RUnlock()

	// Cache miss — take write lock to fetch and store
	lock.Lock()
	defer lock.Unlock()

	// Double-check: another goroutine may have populated the cache while we waited
	if served := proxyServeFromCache(w, r, registryName, key); served {
		if proxyCacheHitsCounter != nil {
			proxyCacheHitsCounter.Add(r.Context(), 1,
				metric.WithAttributes(attribute.String("registry", registryName)))
		}
		return
	}

	if proxyCacheMissesCounter != nil {
		proxyCacheMissesCounter.Add(r.Context(), 1,
			metric.WithAttributes(attribute.String("registry", registryName)))
	}
	proxyFetchAndCache(w, r, reg, remotePath, key)
}

// handleProxyStats returns cache statistics.
func handleProxyStats(w http.ResponseWriter, _ *http.Request) {
	stats := map[string]any{}
	var totalSize int64
	var totalFiles int

	for name := range defaultRegistries {
		regDir := filepath.Join(proxyDir, name)
		var size int64
		var count int
		filepath.Walk(regDir, func(_ string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			if strings.HasSuffix(info.Name(), ".body") {
				size += info.Size()
				count++
			}
			return nil
		})
		stats[name] = map[string]any{"files": count, "size_bytes": size}
		totalSize += size
		totalFiles += count
	}

	stats["total"] = map[string]any{"files": totalFiles, "size_bytes": totalSize}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func proxyCacheKey(registry, path string) string {
	h := sha256.Sum256([]byte(registry + "/" + path))
	return fmt.Sprintf("%x", h)[:16]
}

// proxyServeFromCache attempts to serve a cached response. Returns true if served.
// Caller must hold at least a read lock on the key.
func proxyServeFromCache(w http.ResponseWriter, r *http.Request, registry, key string) bool {
	metaPath := filepath.Join(proxyDir, registry, key+".meta")
	bodyPath := filepath.Join(proxyDir, registry, key+".body")

	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}

	var meta proxyMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return false
	}

	// Check TTL for mutable content
	if !meta.Immutable {
		age := time.Since(time.Unix(meta.CachedAt, 0))
		if age > proxyCacheTTL {
			return false
		}
	}

	// Serve the cached body
	if _, err := os.Stat(bodyPath); err != nil {
		return false
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("X-Proxy-Cache", "HIT")
	w.Header().Set("X-Proxy-Cached-At", time.Unix(meta.CachedAt, 0).Format(time.RFC3339))
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
		return true
	}
	http.ServeFile(w, r, bodyPath)
	return true
}

// proxyFetchAndCache fetches from upstream, caches the response, and writes it to the client.
// Caller must hold the write lock on the key.
func proxyFetchAndCache(w http.ResponseWriter, r *http.Request, reg Registry, remotePath, key string) {
	upstreamURL := reg.Upstream + "/" + remotePath

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("bad upstream URL: %v", err), http.StatusInternalServerError)
		return
	}
	// Forward Accept header so registries return the right content type
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", "sparkwing-proxy/1.0")

	fetchStart := time.Now()
	resp, err := proxyClient.Do(req)
	if err != nil {
		// Try serving stale cache on upstream failure
		if served := proxyServeStale(w, r, reg.Name, key); served {
			return
		}
		http.Error(w, fmt.Sprintf("upstream fetch failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if proxyUpstreamDuration != nil {
		proxyUpstreamDuration.Record(r.Context(), time.Since(fetchStart).Seconds(),
			metric.WithAttributes(attribute.String("registry", reg.Name)))
	}

	if resp.StatusCode >= 400 {
		w.Header().Set("X-Proxy-Cache", "MISS")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 500<<20)) // 500MB max
	if err != nil {
		http.Error(w, fmt.Sprintf("reading upstream: %v", err), http.StatusBadGateway)
		return
	}

	// URL rewriting for npm/pip
	if reg.RewriteBody && len(body) > 0 {
		body = proxyRewriteBody(body, reg, r)
	}

	immutable := isImmutable(remotePath)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Write cache files
	bodyPath := filepath.Join(proxyDir, reg.Name, key+".body")
	metaPath := filepath.Join(proxyDir, reg.Name, key+".meta")

	meta := proxyMeta{
		Path:        remotePath,
		ContentType: contentType,
		CachedAt:    time.Now().Unix(),
		Size:        int64(len(body)),
		Immutable:   immutable,
		StatusCode:  resp.StatusCode,
	}
	metaJSON, _ := json.Marshal(meta)

	// Write atomically via temp files
	tmpBody := bodyPath + ".tmp"
	if err := os.WriteFile(tmpBody, body, 0o644); err != nil {
		log.Printf("warning: proxy cache write error: %v", err)
	} else {
		os.Rename(tmpBody, bodyPath)
		os.WriteFile(metaPath, metaJSON, 0o644)
	}

	log.Printf("proxy: MISS %s/%s (%d bytes, immutable=%v)", reg.Name, truncatePath(remotePath), len(body), immutable)

	// Write response
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Proxy-Cache", "MISS")
	w.Write(body)
}

// proxyServeStale serves an expired cache entry as a fallback when upstream is down.
func proxyServeStale(w http.ResponseWriter, r *http.Request, registry, key string) bool {
	metaPath := filepath.Join(proxyDir, registry, key+".meta")
	bodyPath := filepath.Join(proxyDir, registry, key+".body")

	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}
	var meta proxyMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return false
	}
	if _, err := os.Stat(bodyPath); err != nil {
		return false
	}

	log.Printf("proxy: STALE %s/%s (upstream down)", registry, truncatePath(meta.Path))
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("X-Proxy-Cache", "STALE")
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
		return true
	}
	http.ServeFile(w, r, bodyPath)
	return true
}

// proxyRewriteBody replaces upstream URLs with proxy URLs in response bodies.
// For npm: rewrites tarball URLs in metadata JSON.
// For pypi: rewrites file download URLs in simple index HTML.
func proxyRewriteBody(body []byte, reg Registry, r *http.Request) []byte {
	// Build the proxy base URL from the incoming request
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = r.Header.Get("X-Forwarded-Host")
	}
	proxyBase := fmt.Sprintf("%s://%s/proxy", scheme, host)

	s := string(body)

	switch reg.Name {
	case "npm":
		// npm metadata contains tarball URLs like:
		//   "tarball": "https://registry.npmjs.org/<pkg>/-/<pkg>-<ver>.tgz"
		// Rewrite to: "tarball": "http://<host>/proxy/npm/<pkg>/-/<pkg>-<ver>.tgz"
		s = strings.ReplaceAll(s, reg.Upstream, proxyBase+"/npm")

	case "pypi":
		// pip simple index HTML contains links like:
		//   href="https://files.pythonhosted.org/packages/..."
		// Rewrite to: href="http://<host>/proxy/pythonhosted/packages/..."
		s = strings.ReplaceAll(s, "https://files.pythonhosted.org", proxyBase+"/pythonhosted")
	}

	return []byte(s)
}

// isImmutable returns true for file extensions that represent versioned, immutable artifacts.
func isImmutable(path string) bool {
	immutableExts := []string{
		".tgz", ".tar.gz", ".whl", ".gem", ".zip", ".jar",
		".crate", ".apk", ".deb", ".rpm", ".egg", ".nupkg",
	}
	lower := strings.ToLower(path)
	for _, ext := range immutableExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// proxyCleanupLoop runs periodically and removes expired cache entries.
func proxyCleanupLoop() {
	interval := 1 * time.Hour
	log.Printf("proxy cleanup: every %s, max age %s", interval, proxyMaxAge)

	for {
		time.Sleep(interval)
		removed := 0

		for name := range defaultRegistries {
			regDir := filepath.Join(proxyDir, name)
			entries, err := os.ReadDir(regDir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !strings.HasSuffix(e.Name(), ".meta") {
					continue
				}
				metaPath := filepath.Join(regDir, e.Name())
				data, err := os.ReadFile(metaPath)
				if err != nil {
					continue
				}
				var meta proxyMeta
				if err := json.Unmarshal(data, &meta); err != nil {
					continue
				}

				age := time.Since(time.Unix(meta.CachedAt, 0))

				// Remove mutable entries past 10x TTL, immutable past max age
				var expired bool
				if meta.Immutable {
					expired = age > proxyMaxAge
				} else {
					expired = age > proxyCacheTTL*10
				}

				if expired {
					key := strings.TrimSuffix(e.Name(), ".meta")
					os.Remove(metaPath)
					os.Remove(filepath.Join(regDir, key+".body"))
					removed++
				}
			}
		}
		if removed > 0 {
			log.Printf("proxy cleanup: removed %d expired entries", removed)
		}
	}
}

func registryList() string {
	names := make([]string, 0, len(defaultRegistries))
	for name := range defaultRegistries {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

func truncatePath(path string) string {
	if len(path) > 80 {
		return path[:77] + "..."
	}
	return path
}
