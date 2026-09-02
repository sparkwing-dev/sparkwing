package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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

const (
	proxyRewriteChunk = 64 << 10
	proxyHostError    = "missing or malformed Host header"
)

var proxyHostPattern = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?(\.[A-Za-z0-9_]([A-Za-z0-9_-]*[A-Za-z0-9_])?)*$`)

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

	// #nosec G703 -- the path is a registry name from the hard-coded table plus a sha256 cache key
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

	// #nosec G703 -- the path is a registry name from the hard-coded table plus a sha256 cache key
	if _, err := os.Stat(bodyPath); err != nil {
		return false
	}

	return proxyWriteCachedBody(w, r, registry, meta, bodyPath, "HIT")
}

func proxyFetchAndCache(w http.ResponseWriter, r *http.Request, reg Registry, remotePath, key string) {
	upstreamURL := reg.Upstream + "/" + remotePath

	// #nosec G704 -- the host comes from the hard-coded registry table; only the path is caller-supplied
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
	// #nosec G704 -- the host comes from the hard-coded registry table; only the path is caller-supplied
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
	perRequest := false
	if proxyShouldRewrite(reg, immutable) && len(body) > 0 {
		if proxyPublicBase != "" {
			stored = proxyRewriteBody(body, reg, proxyPublicBase)
			served = stored
		} else {
			base, ok := proxyBaseForRequest(r)
			if !ok {
				http.Error(w, proxyHostError, http.StatusBadRequest)
				return
			}
			// safety: the cached copy stays unrewritten so a forged Host cannot reach another client.
			served = proxyRewriteBody(body, reg, base)
			perRequest = true
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

	// #nosec G706 -- %q escapes control characters in the caller-supplied path
	log.Printf("proxy: MISS %s/%q (%d bytes, immutable=%v)", reg.Name, truncatePath(remotePath), len(stored), immutable)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Proxy-Cache", "MISS")
	proxySetCacheability(w, meta, perRequest)
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

	if !proxyWriteCachedBody(w, r, registry, meta, bodyPath, "STALE") {
		return false
	}
	log.Printf("proxy: STALE %s/%q (upstream down)", registry, truncatePath(meta.Path))
	return true
}

func proxyWriteCachedBody(w http.ResponseWriter, r *http.Request, registry string, meta proxyMeta, bodyPath, status string) bool {
	reg, known := defaultRegistries[registry]
	rewrite := proxyPublicBase == "" && known && proxyShouldRewrite(reg, meta.Immutable) && meta.Size > 0

	base := ""
	var src *os.File
	if rewrite {
		var ok bool
		if base, ok = proxyBaseForRequest(r); !ok {
			http.Error(w, proxyHostError, http.StatusBadRequest)
			return true
		}
		// perf: HEAD answers from the metadata alone, so a large mutable entry is never opened or read.
		if r.Method != http.MethodHead {
			// #nosec G703 -- the path is a registry name from the hard-coded table plus a sha256 cache key
			f, err := os.Open(bodyPath)
			if err != nil {
				return false
			}
			defer f.Close()
			src = f
		}
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("X-Proxy-Cache", status)
	w.Header().Set("X-Proxy-Cached-At", time.Unix(meta.CachedAt, 0).Format(time.RFC3339))
	proxySetCacheability(w, meta, rewrite)

	if !rewrite {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
			return true
		}
		// #nosec G703 -- the path is a registry name from the hard-coded table plus a sha256 cache key
		http.ServeFile(w, r, bodyPath)
		return true
	}
	if src == nil {
		return true
	}

	old, replacement := proxyRewriteRule(reg, base)
	if err := proxyStreamReplace(w, src, old, replacement); err != nil {
		// #nosec G706 -- %q escapes control characters in the caller-supplied path
		log.Printf("warning: proxy rewrite stream %s/%s: %v", registry, truncatePath(meta.Path), err)
	}
	return true
}

func proxySetCacheability(w http.ResponseWriter, meta proxyMeta, perRequestRewrite bool) {
	if perRequestRewrite {
		// safety: the body carries this request's own Host, so no shared cache may hand it to another client.
		vary := "Host"
		if proxyTrustForwardedHost {
			vary = "Host, X-Forwarded-Host"
		}
		w.Header().Set("Vary", vary)
		w.Header().Set("Cache-Control", "private, max-age=0")
		return
	}
	ttl := proxyCacheTTL
	if meta.Immutable {
		ttl = proxyMaxAge
	}
	remaining := ttl - time.Since(time.Unix(meta.CachedAt, 0))
	if remaining < 0 {
		remaining = 0
	}
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int64((remaining+time.Second-1)/time.Second)))
}

func proxyStreamReplace(dst io.Writer, src io.Reader, old, replacement string) error {
	if old == "" {
		_, err := io.Copy(dst, src)
		return err
	}
	oldBytes, newBytes := []byte(old), []byte(replacement)
	buf := make([]byte, proxyRewriteChunk)
	window := make([]byte, 0, proxyRewriteChunk+len(oldBytes))
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			window = append(window, buf[:n]...)
			rest, err := proxyFlushReplaced(dst, window, oldBytes, newBytes)
			if err != nil {
				return err
			}
			window = append(window[:0], rest...)
		}
		if readErr == io.EOF {
			_, err := dst.Write(bytes.ReplaceAll(window, oldBytes, newBytes))
			return err
		}
		if readErr != nil {
			return readErr
		}
	}
}

func proxyFlushReplaced(dst io.Writer, window, old, replacement []byte) ([]byte, error) {
	pos := 0
	for {
		idx := bytes.Index(window[pos:], old)
		if idx < 0 {
			break
		}
		if _, err := dst.Write(window[pos : pos+idx]); err != nil {
			return nil, err
		}
		if _, err := dst.Write(replacement); err != nil {
			return nil, err
		}
		pos += idx + len(old)
	}
	// safety: the last len(old)-1 bytes stay buffered so a match split across two reads is still replaced.
	keep := len(old) - 1
	if keep > len(window)-pos {
		keep = len(window) - pos
	}
	if _, err := dst.Write(window[pos : len(window)-keep]); err != nil {
		return nil, err
	}
	return window[len(window)-keep:], nil
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
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("cache: public URL must carry no query or fragment, got %q", raw)
	}
	if path := strings.TrimSuffix(u.Path, "/"); path != "" && path != "/proxy" {
		return "", fmt.Errorf("cache: public URL must be a scheme and host with no path beyond /proxy, got %q", raw)
	}
	return u.Scheme + "://" + u.Host + "/proxy", nil
}

func proxyBaseForRequest(r *http.Request) (string, bool) {
	if proxyPublicBase != "" {
		return proxyPublicBase, true
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	// safety: forwarded headers are caller-controlled unless a trusted reverse proxy is the only way in.
	if proxyTrustForwardedHost {
		if fwd := lastForwardedValue(r.Header.Get("X-Forwarded-Host")); fwd != "" {
			host = fwd
		}
		if proto := lastForwardedValue(r.Header.Get("X-Forwarded-Proto")); proto == "http" || proto == "https" {
			scheme = proto
		}
	}
	if !validProxyHost(host) {
		return "", false
	}
	return scheme + "://" + host + "/proxy", true
}

func lastForwardedValue(header string) string {
	idx := strings.LastIndex(header, ",")
	// safety: proxies append, so the right-most element is the nearest trusted hop and the left-most is the client's.
	return strings.TrimSpace(header[idx+1:])
}

func validProxyHost(host string) bool {
	if host == "" || len(host) > 255 {
		return false
	}
	name := host
	if h, port, err := net.SplitHostPort(host); err == nil {
		if port == "" || strings.TrimLeft(port, "0123456789") != "" {
			return false
		}
		name = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		name = host[1 : len(host)-1]
	}
	if strings.Contains(name, ":") {
		return net.ParseIP(name) != nil
	}
	return proxyHostPattern.MatchString(strings.TrimSuffix(name, "."))
}

func proxyRewriteRule(reg Registry, proxyBase string) (string, string) {
	switch reg.Name {
	case "npm":
		return reg.Upstream, proxyBase + "/npm"
	case "pypi":
		return "https://files.pythonhosted.org", proxyBase + "/pythonhosted"
	}
	return "", ""
}

func proxyRewriteBody(body []byte, reg Registry, proxyBase string) []byte {
	old, replacement := proxyRewriteRule(reg, proxyBase)
	if old == "" {
		return body
	}
	return bytes.ReplaceAll(body, []byte(old), []byte(replacement))
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
