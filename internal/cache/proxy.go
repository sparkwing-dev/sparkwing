package cache

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type Registry struct {
	Name        string
	Upstream    string
	RewriteBody bool
}

var defaultRegistries = map[string]Registry{
	"npm":          {Name: "npm", Upstream: "https://registry.npmjs.org", RewriteBody: true},
	"pypi":         {Name: "pypi", Upstream: "https://pypi.org", RewriteBody: true},
	"pythonhosted": {Name: "pythonhosted", Upstream: "https://files.pythonhosted.org", RewriteBody: false},
	"rubygems":     {Name: "rubygems", Upstream: "https://rubygems.org", RewriteBody: false},
	"golang":       {Name: "golang", Upstream: "https://proxy.golang.org", RewriteBody: false},
	"alpine":       {Name: "alpine", Upstream: "https://dl-cdn.alpinelinux.org", RewriteBody: false},
}

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
	proxyMaxAge   = 7 * 24 * time.Hour
	proxyClient   = &http.Client{Timeout: 60 * time.Second}

	proxyKeyLocks   = map[string]*sync.RWMutex{}
	proxyKeyLocksMu sync.Mutex

	proxyPublicBase         string
	proxyTrustForwardedHost bool
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
	for name := range defaultRegistries {
		if err := os.MkdirAll(filepath.Join(proxyDir, name), 0o755); err != nil {
			log.Printf("warning: proxy init mkdir %s: %v", name, err)
		}
	}
}

func handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET or HEAD only", http.StatusMethodNotAllowed)
		return
	}

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
		http.Error(w, fmt.Sprintf("unknown registry %q -- supported: %s", registryName, registryList()), http.StatusBadRequest)
		return
	}

	if strings.Contains(remotePath, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	key := proxyCacheKey(registryName, remotePath)
	lock := proxyKeyLock(key)

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

	lock.Lock()
	defer lock.Unlock()

	// safety: double-checked locking -- another goroutine may have populated the cache while we waited.
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

func handleProxyStats(w http.ResponseWriter, _ *http.Request) {
	stats := map[string]any{}
	var totalSize int64
	var totalFiles int

	for name := range defaultRegistries {
		regDir := filepath.Join(proxyDir, name)
		var size int64
		var count int
		if err := filepath.Walk(regDir, func(_ string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			if strings.HasSuffix(info.Name(), ".body") {
				size += info.Size()
				count++
			}
			return nil
		}); err != nil {
			log.Printf("warning: proxy stats walk %s: %v", name, err)
		}
		stats[name] = map[string]any{"files": count, "size_bytes": size}
		totalSize += size
		totalFiles += count
	}

	stats["total"] = map[string]any{"files": totalFiles, "size_bytes": totalSize}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(stats); err != nil {
		log.Printf("warning: proxy stats encode: %v", err)
	}
}

func proxyCacheKey(registry, path string) string {
	h := sha256.Sum256([]byte(strconv.Itoa(len(registry)) + ":" + registry + "/" + path))
	return fmt.Sprintf("%x", h)[:16]
}

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

	if !meta.Immutable {
		age := time.Since(time.Unix(meta.CachedAt, 0))
		if age > proxyCacheTTL {
			return false
		}
	}

	if _, err := os.Stat(bodyPath); err != nil {
		return false
	}

	rewritten, ok := proxyCachedRewrite(registry, meta, bodyPath, r)
	if !ok {
		return false
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("X-Proxy-Cache", "HIT")
	w.Header().Set("X-Proxy-Cached-At", time.Unix(meta.CachedAt, 0).Format(time.RFC3339))
	proxyWriteCachedBody(w, r, meta, bodyPath, rewritten)
	return true
}

func proxyFetchAndCache(w http.ResponseWriter, r *http.Request, reg Registry, remotePath, key string) {
	upstreamURL := reg.Upstream + "/" + remotePath

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("bad upstream URL: %v", err), http.StatusInternalServerError)
		return
	}
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", "sparkwing-proxy/1.0")

	fetchStart := time.Now()
	resp, err := proxyClient.Do(req)
	if err != nil {
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
		_, _ = io.Copy(w, resp.Body)
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 500<<20))
	if err != nil {
		http.Error(w, fmt.Sprintf("reading upstream: %v", err), http.StatusBadGateway)
		return
	}

	immutable := isImmutable(remotePath)
	stored, served := body, body
	if proxyShouldRewrite(reg, immutable) && len(body) > 0 {
		if proxyPublicBase != "" {
			stored = proxyRewriteBody(body, reg, proxyPublicBase)
			served = stored
		} else {
			// safety: the cached copy stays unrewritten so a forged Host cannot reach another client.
			served = proxyRewriteBody(body, reg, proxyBaseForRequest(r))
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	bodyPath := filepath.Join(proxyDir, reg.Name, key+".body")
	metaPath := filepath.Join(proxyDir, reg.Name, key+".meta")

	meta := proxyMeta{
		Path:        remotePath,
		ContentType: contentType,
		CachedAt:    time.Now().Unix(),
		Size:        int64(len(stored)),
		Immutable:   immutable,
		StatusCode:  resp.StatusCode,
	}
	metaJSON, _ := json.Marshal(meta)

	tmpBody := bodyPath + ".tmp"
	if err := os.WriteFile(tmpBody, stored, 0o644); err != nil {
		log.Printf("warning: proxy cache write error: %v", err)
	} else {
		if err := os.Rename(tmpBody, bodyPath); err != nil {
			log.Printf("warning: proxy cache rename: %v", err)
		}
		if err := os.WriteFile(metaPath, metaJSON, 0o644); err != nil {
			log.Printf("warning: proxy cache meta write: %v", err)
		}
	}

	log.Printf("proxy: MISS %s/%s (%d bytes, immutable=%v)", reg.Name, truncatePath(remotePath), len(stored), immutable)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Proxy-Cache", "MISS")
	w.Write(served)
}

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

	rewritten, ok := proxyCachedRewrite(registry, meta, bodyPath, r)
	if !ok {
		return false
	}

	log.Printf("proxy: STALE %s/%s (upstream down)", registry, truncatePath(meta.Path))
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("X-Proxy-Cache", "STALE")
	proxyWriteCachedBody(w, r, meta, bodyPath, rewritten)
	return true
}

func proxyCachedRewrite(registry string, meta proxyMeta, bodyPath string, r *http.Request) ([]byte, bool) {
	if proxyPublicBase != "" {
		return nil, true
	}
	reg, known := defaultRegistries[registry]
	if !known || !proxyShouldRewrite(reg, meta.Immutable) {
		return nil, true
	}
	raw, err := os.ReadFile(bodyPath)
	if err != nil {
		return nil, false
	}
	if len(raw) == 0 {
		return nil, true
	}
	return proxyRewriteBody(raw, reg, proxyBaseForRequest(r)), true
}

func proxyWriteCachedBody(w http.ResponseWriter, r *http.Request, meta proxyMeta, bodyPath string, rewritten []byte) {
	if rewritten != nil {
		w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(rewritten)
		}
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
		return
	}
	http.ServeFile(w, r, bodyPath)
}

func proxyShouldRewrite(reg Registry, immutable bool) bool {
	// perf: content-addressed artifacts carry no upstream URLs and can be huge, so they stream from disk untouched.
	return reg.RewriteBody && !immutable
}

func normalizeProxyPublicBase(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("cache: invalid public URL %q: %w", raw, err)
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("cache: public URL must be an absolute http or https URL, got %q", raw)
	}
	base := strings.TrimSuffix(u.Scheme+"://"+u.Host+u.Path, "/")
	return strings.TrimSuffix(base, "/proxy") + "/proxy", nil
}

func proxyBaseForRequest(r *http.Request) string {
	if proxyPublicBase != "" {
		return proxyPublicBase
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	// safety: forwarded headers are caller-controlled unless a trusted reverse proxy is the only way in.
	if proxyTrustForwardedHost {
		if fwd := firstForwardedValue(r.Header.Get("X-Forwarded-Host")); fwd != "" {
			host = fwd
		}
		if proto := firstForwardedValue(r.Header.Get("X-Forwarded-Proto")); proto == "http" || proto == "https" {
			scheme = proto
		}
	}
	return fmt.Sprintf("%s://%s/proxy", scheme, host)
}

func firstForwardedValue(header string) string {
	first, _, _ := strings.Cut(header, ",")
	return strings.TrimSpace(first)
}

func proxyRewriteBody(body []byte, reg Registry, proxyBase string) []byte {
	s := string(body)

	switch reg.Name {
	case "npm":
		s = strings.ReplaceAll(s, reg.Upstream, proxyBase+"/npm")

	case "pypi":
		s = strings.ReplaceAll(s, "https://files.pythonhosted.org", proxyBase+"/pythonhosted")
	}

	return []byte(s)
}

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

func proxyCleanupLoop(ctx context.Context) {
	interval := 1 * time.Hour
	log.Printf("proxy cleanup: every %s, max age %s", interval, proxyMaxAge)

	for {
		if !sleepCtx(ctx, interval) {
			return
		}
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

				var expired bool
				if meta.Immutable {
					expired = age > proxyMaxAge
				} else {
					expired = age > proxyCacheTTL*10
				}

				if expired {
					key := strings.TrimSuffix(e.Name(), ".meta")
					_ = os.Remove(metaPath)
					_ = os.Remove(filepath.Join(regDir, key+".body"))
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
