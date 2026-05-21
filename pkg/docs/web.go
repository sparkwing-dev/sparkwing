package docs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultBaseURL is the production host. Override via
// SPARKWING_DOCS_BASE_URL (read by NewWebClient) for testing or
// against a staging site. The override is honored as-is — callers
// pinning a custom BaseURL via the struct field also bypass the env
// lookup.
const DefaultBaseURL = "https://sparkwing.dev"

// BaseURLEnvVar names the env var that overrides DefaultBaseURL when
// constructing a WebClient via NewWebClient. Empty / unset means use
// the default.
const BaseURLEnvVar = "SPARKWING_DOCS_BASE_URL"

// IndexTTL is the freshness window for *.json index files
// (versions.json and per-version index.json). Per-version markdown
// content is cached indefinitely because versioned tags are
// immutable — once v0.3.0/pipelines.md is published, it never
// changes.
const IndexTTL = 24 * time.Hour

// WebClient fetches docs / migrations / versions from sparkwing.dev.
// Stateless aside from the on-disk cache directory. Concurrency-safe
// across multiple goroutines and processes (writes go through
// temp-file + rename).
//
// Zero value is not usable; construct via NewWebClient (which honors
// SPARKWING_DOCS_BASE_URL) or by setting BaseURL / HTTP / CacheDir
// explicitly.
type WebClient struct {
	BaseURL   string
	HTTP      *http.Client
	CacheDir  string
	NoCache   bool
	UserAgent string
}

// Versions is the shape of /versions.json. `Versions` is ordered
// newest-first by the server contract.
type Versions struct {
	Latest   string   `json:"latest"`
	Versions []string `json:"versions"`
}

// LatestAlias is the sentinel passed to Doc / DocIndex etc. for the
// unversioned URL (`/docs/<slug>.md` rather than
// `/docs/v0.3.0/<slug>.md`). Skips per-version path resolution and
// the discovery roundtrip.
const LatestAlias = "latest"

// ErrUnavailable signals a transport-level failure (network error,
// non-404 5xx after retry). Distinct from ErrNotFound so callers can
// phrase the right user-facing message.
var ErrUnavailable = errors.New("docs: web fetch unavailable")

// NewWebClient returns a WebClient with sensible defaults: 10s HTTP
// timeout, one retry on 5xx / connection error, host-locked redirect
// policy, UA carrying CLI + Go version, cache in
// $XDG_CACHE_HOME/sparkwing/web/ (or ~/.cache/sparkwing/web/).
//
// SPARKWING_DOCS_BASE_URL, if set, overrides DefaultBaseURL. Empty /
// unset falls back to the default.
func NewWebClient() *WebClient {
	base := strings.TrimRight(os.Getenv(BaseURLEnvVar), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &WebClient{
		BaseURL:   base,
		HTTP:      defaultHTTPClient(),
		CacheDir:  DefaultCacheDir(),
		UserAgent: defaultUserAgent(),
	}
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		// Redirect policy: max 5 hops; reject cross-host so a
		// compromised CDN can't poison the cache with content from an
		// unrelated origin.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			if len(via) == 0 {
				return nil
			}
			orig, dst := via[0].URL.Host, req.URL.Host
			if orig != dst {
				return fmt.Errorf("refusing cross-host redirect: %s -> %s", orig, dst)
			}
			return nil
		},
	}
}

func defaultUserAgent() string {
	v := Version
	if v == "" {
		v = "dev"
	}
	return fmt.Sprintf("sparkwing/%s (https://sparkwing.dev) go-%s", v, runtime.Version())
}

// Version is set by the CLI at startup (cmd/sparkwing wires its own
// installedVersion() into this var via an init hook) so the web
// client's User-Agent carries the binary's version. Empty during
// library-only use; the UA falls back to "sparkwing/dev".
var Version string

// DefaultCacheDir returns $XDG_CACHE_HOME/sparkwing/web (or, when
// XDG_CACHE_HOME is unset, ~/.cache/sparkwing/web). Returns empty
// string if neither $XDG_CACHE_HOME nor $HOME resolves; callers then
// run uncached.
func DefaultCacheDir() string {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "sparkwing", "web")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", "sparkwing", "web")
	}
	return ""
}

// Versions fetches /versions.json with a 24h cache TTL.
func (c *WebClient) Versions(ctx context.Context) (Versions, error) {
	body, err := c.fetch(ctx, "/versions.json", fetchOpts{ttl: IndexTTL})
	if err != nil {
		return Versions{}, err
	}
	var v Versions
	if err := json.Unmarshal(body, &v); err != nil {
		return Versions{}, fmt.Errorf("docs: parse versions.json: %w", err)
	}
	return v, nil
}

// Doc fetches a single doc's markdown. version=="" or version==LatestAlias
// uses the unversioned URL (/docs/<slug>.md). Otherwise uses
// /docs/<version>/<slug>.md. Slug must not contain ".." or absolute
// paths; the slug is normalized but malformed input errors out
// rather than escaping the docs prefix.
func (c *WebClient) Doc(ctx context.Context, version, slug string) (string, error) {
	clean, err := normalizeSlug(slug)
	if err != nil {
		return "", err
	}
	p := webDocPath(version, clean)
	body, err := c.fetch(ctx, p, fetchOpts{})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// DocIndex fetches the per-version doc index (24h TTL). Empty or
// "latest" version uses /docs/index.json.
func (c *WebClient) DocIndex(ctx context.Context, version string) ([]Entry, error) {
	p := webDocIndexPath(version)
	body, err := c.fetch(ctx, p, fetchOpts{ttl: IndexTTL})
	if err != nil {
		return nil, err
	}
	var entries []Entry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("docs: parse %s: %w", p, err)
	}
	return entries, nil
}

// Migration fetches /migrations/<version>.md. version must be a valid
// semver tag (e.g. v0.4.0); LatestAlias isn't meaningful for
// migrations and is rejected.
func (c *WebClient) Migration(ctx context.Context, version string) (string, error) {
	if version == "" || version == LatestAlias {
		return "", fmt.Errorf("docs: Migration: explicit semver version required (got %q)", version)
	}
	p := "/migrations/" + version + ".md"
	body, err := c.fetch(ctx, p, fetchOpts{})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// MigrationIndex fetches /migrations/index.json (24h TTL).
func (c *WebClient) MigrationIndex(ctx context.Context) ([]MigrationEntry, error) {
	body, err := c.fetch(ctx, "/migrations/index.json", fetchOpts{ttl: IndexTTL})
	if err != nil {
		return nil, err
	}
	var entries []MigrationEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("docs: parse /migrations/index.json: %w", err)
	}
	return entries, nil
}

// webDocPath returns the URL path for a doc. version=="latest" or ""
// means the unversioned alias.
func webDocPath(version, slug string) string {
	if version == "" || version == LatestAlias {
		return "/docs/" + slug + ".md"
	}
	return "/docs/" + version + "/" + slug + ".md"
}

func webDocIndexPath(version string) string {
	if version == "" || version == LatestAlias {
		return "/docs/index.json"
	}
	return "/docs/" + version + "/index.json"
}

// normalizeSlug rejects slugs that would escape the /docs/ prefix
// (absolute paths, traversal segments, trailing .md). Returns the
// canonical slug (no leading slash, no .md suffix).
func normalizeSlug(slug string) (string, error) {
	if slug == "" {
		return "", errors.New("docs: empty slug")
	}
	slug = strings.TrimSuffix(slug, ".md")
	if strings.HasPrefix(slug, "/") {
		return "", fmt.Errorf("docs: slug %q must not be absolute", slug)
	}
	for _, part := range strings.Split(slug, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("docs: slug %q contains an invalid segment", slug)
		}
	}
	return slug, nil
}

// fetchOpts tunes the cache+fetch behavior for one call. ttl==0 means
// "indefinite" (per-version content); positive ttl means "refetch
// after this long" (index files).
type fetchOpts struct {
	ttl time.Duration
}

// cacheMeta is the JSON sidecar written alongside each cached body.
// fetched_at lets TTL checks survive across filesystems that
// round-trip mtime poorly; content_type / etag / status are
// informational and surface from `sparkwing docs cache info`.
type cacheMeta struct {
	FetchedAt   time.Time `json:"fetched_at"`
	ContentType string    `json:"content_type,omitempty"`
	ETag        string    `json:"etag,omitempty"`
	Status      int       `json:"status"`
	URL         string    `json:"url"`
}

// fetch is the shared cache-aware HTTP helper. Path is a server-rooted
// path like "/versions.json" or "/docs/v0.3.0/pipelines.md".
func (c *WebClient) fetch(ctx context.Context, urlPath string, opts fetchOpts) ([]byte, error) {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	if !c.NoCache && c.CacheDir != "" {
		if body, ok := c.readCache(urlPath, opts.ttl); ok {
			return body, nil
		}
	}
	full := base + urlPath
	body, meta, err := c.httpGetWithRetry(ctx, full)
	if err != nil {
		return nil, err
	}
	if !c.NoCache && c.CacheDir != "" {
		_ = c.writeCache(urlPath, body, meta)
	}
	return body, nil
}

// httpGetWithRetry does one retry on 5xx / connection errors. 4xx is
// returned immediately as ErrNotFound (404) or wrapped in
// ErrUnavailable (other 4xx).
func (c *WebClient) httpGetWithRetry(ctx context.Context, fullURL string) ([]byte, cacheMeta, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		body, meta, err := c.httpGet(ctx, fullURL)
		if err == nil {
			return body, meta, nil
		}
		// 4xx is terminal; retry only for transport errors / 5xx.
		if errors.Is(err, ErrNotFound) {
			return nil, cacheMeta{}, err
		}
		var s *statusErr
		if errors.As(err, &s) && s.code >= 400 && s.code < 500 {
			return nil, cacheMeta{}, err
		}
		lastErr = err
	}
	return nil, cacheMeta{}, fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
}

// statusErr lets callers branch on HTTP status without parsing the
// error string. Wrapped in ErrUnavailable by httpGetWithRetry for
// non-404 failures.
type statusErr struct {
	code int
	url  string
}

func (e *statusErr) Error() string {
	return fmt.Sprintf("docs: GET %s returned %d", e.url, e.code)
}

func (c *WebClient) httpGet(ctx context.Context, fullURL string) ([]byte, cacheMeta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, cacheMeta{}, err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	req.Header.Set("Accept", "application/json, text/markdown, */*;q=0.5")
	client := c.HTTP
	if client == nil {
		client = defaultHTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, cacheMeta{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, cacheMeta{}, fmt.Errorf("%w: %s", ErrNotFound, fullURL)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, cacheMeta{}, &statusErr{code: resp.StatusCode, url: fullURL}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, cacheMeta{}, err
	}
	return body, cacheMeta{
		FetchedAt:   time.Now().UTC(),
		ContentType: resp.Header.Get("Content-Type"),
		ETag:        resp.Header.Get("ETag"),
		Status:      resp.StatusCode,
		URL:         fullURL,
	}, nil
}

// cachePath maps a URL path into an on-disk file path under CacheDir.
// Returns "" if the result would escape CacheDir. The body file lives
// at <CacheDir>/<urlPath>; the sidecar at <body>.meta.
func (c *WebClient) cachePath(urlPath string) string {
	if c.CacheDir == "" {
		return ""
	}
	cleanURL, err := url.Parse(urlPath)
	if err != nil {
		return ""
	}
	cleaned := filepath.Clean("/" + cleanURL.Path)
	target := filepath.Join(c.CacheDir, cleaned)
	rel, err := filepath.Rel(c.CacheDir, target)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return ""
	}
	return target
}

func (c *WebClient) readCache(urlPath string, ttl time.Duration) ([]byte, bool) {
	bodyPath := c.cachePath(urlPath)
	if bodyPath == "" {
		return nil, false
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		return nil, false
	}
	if ttl > 0 {
		metaBytes, merr := os.ReadFile(bodyPath + ".meta")
		if merr != nil {
			// No sidecar: treat as expired. Reader can rewrite it on
			// next fetch.
			return nil, false
		}
		var m cacheMeta
		if jerr := json.Unmarshal(metaBytes, &m); jerr != nil {
			return nil, false
		}
		if time.Since(m.FetchedAt) > ttl {
			return nil, false
		}
	}
	return body, true
}

func (c *WebClient) writeCache(urlPath string, body []byte, meta cacheMeta) error {
	bodyPath := c.cachePath(urlPath)
	if bodyPath == "" {
		return errors.New("docs: cache path resolves outside CacheDir")
	}
	if err := os.MkdirAll(filepath.Dir(bodyPath), 0o755); err != nil {
		return err
	}
	if err := writeAtomic(bodyPath, body, 0o644); err != nil {
		return err
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(bodyPath+".meta", metaBytes, 0o644)
}

// writeAtomic writes data to a temp file in the same directory then
// renames into place. Survives concurrent invocations by giving each
// writer a distinct temp file (os.CreateTemp uses random suffixes).
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanupTmp := func() { _ = os.Remove(tmpName) }
	if _, werr := tmp.Write(data); werr != nil {
		_ = tmp.Close()
		cleanupTmp()
		return werr
	}
	if cerr := tmp.Close(); cerr != nil {
		cleanupTmp()
		return cerr
	}
	if cherr := os.Chmod(tmpName, mode); cherr != nil {
		cleanupTmp()
		return cherr
	}
	if rerr := os.Rename(tmpName, path); rerr != nil {
		cleanupTmp()
		return rerr
	}
	return nil
}

// ClearCache removes everything under CacheDir. Refuses to delete
// anything outside the configured cache directory: it walks via
// filepath.Walk rather than RemoveAll(parent), and double-checks
// each entry is rooted at CacheDir before unlinking. Returns the
// number of files removed.
func (c *WebClient) ClearCache() (int, error) {
	if c.CacheDir == "" {
		return 0, errors.New("docs: cache dir not configured")
	}
	info, err := os.Stat(c.CacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("docs: cache path %s is not a directory", c.CacheDir)
	}
	absCache, err := filepath.Abs(c.CacheDir)
	if err != nil {
		return 0, err
	}
	removed := 0
	err = filepath.Walk(c.CacheDir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == c.CacheDir {
			return nil
		}
		abs, aerr := filepath.Abs(p)
		if aerr != nil {
			return aerr
		}
		if !strings.HasPrefix(abs, absCache+string(filepath.Separator)) {
			return fmt.Errorf("docs: refusing to remove path outside cache: %s", abs)
		}
		if info.IsDir() {
			return nil
		}
		if rerr := os.Remove(p); rerr != nil {
			return rerr
		}
		removed++
		return nil
	})
	if err != nil {
		return removed, err
	}
	// Best-effort: prune now-empty subdirectories.
	_ = filepath.Walk(c.CacheDir, func(p string, info os.FileInfo, _ error) error {
		if !info.IsDir() || p == c.CacheDir {
			return nil
		}
		_ = os.Remove(p)
		return nil
	})
	return removed, nil
}

// CacheStats summarizes what's currently stored under CacheDir.
// Surface from `sparkwing docs cache info`.
type CacheStats struct {
	Dir            string `json:"dir"`
	Exists         bool   `json:"exists"`
	TotalFiles     int    `json:"total_files"`
	TotalBytes     int64  `json:"total_bytes"`
	DocFiles       int    `json:"doc_files"`
	MigrationFiles int    `json:"migration_files"`
	IndexFiles     int    `json:"index_files"`
	VersionsState  string `json:"versions_state"`
}

// CacheInfo walks CacheDir and returns a summary. Never errors on a
// missing cache; the zero-stat result tells the caller nothing's
// cached yet.
func (c *WebClient) CacheInfo() (CacheStats, error) {
	s := CacheStats{Dir: c.CacheDir, VersionsState: "absent"}
	if c.CacheDir == "" {
		return s, nil
	}
	info, err := os.Stat(c.CacheDir)
	if err != nil || !info.IsDir() {
		return s, nil
	}
	s.Exists = true
	err = filepath.Walk(c.CacheDir, func(p string, fi os.FileInfo, werr error) error {
		if werr != nil || fi.IsDir() {
			return werr
		}
		if strings.HasSuffix(p, ".meta") {
			return nil
		}
		s.TotalFiles++
		s.TotalBytes += fi.Size()
		rel, _ := filepath.Rel(c.CacheDir, p)
		rel = filepath.ToSlash(rel)
		switch {
		case rel == "versions.json":
			meta, mok := readMeta(p + ".meta")
			if !mok {
				s.VersionsState = "present (no sidecar)"
			} else if time.Since(meta.FetchedAt) > IndexTTL {
				s.VersionsState = fmt.Sprintf("stale (fetched %s ago)", time.Since(meta.FetchedAt).Round(time.Minute))
			} else {
				s.VersionsState = fmt.Sprintf("fresh (fetched %s ago)", time.Since(meta.FetchedAt).Round(time.Minute))
			}
		case strings.HasSuffix(rel, "/index.json"):
			s.IndexFiles++
		case strings.HasPrefix(rel, "migrations/") && strings.HasSuffix(rel, ".md"):
			s.MigrationFiles++
		case strings.HasPrefix(rel, "docs/") && strings.HasSuffix(rel, ".md"):
			s.DocFiles++
		}
		return nil
	})
	return s, err
}

func readMeta(path string) (cacheMeta, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return cacheMeta{}, false
	}
	var m cacheMeta
	if err := json.Unmarshal(body, &m); err != nil {
		return cacheMeta{}, false
	}
	return m, true
}
