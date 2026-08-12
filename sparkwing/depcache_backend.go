package sparkwing

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// remoteDepCacheMaxBytes matches the cache service's PUT /cache/<key>
// MaxBytesReader; a larger archive is skipped client-side with a
// warning instead of failing mid-upload. A var so tests can shrink it.
var remoteDepCacheMaxBytes = int64(500 << 20)

// depCacheHTTPTimeout bounds one remote cache operation. Module
// caches run to hundreds of MB; a laptop-grade link needs minutes,
// not seconds.
const depCacheHTTPTimeout = 10 * time.Minute

// depCacheBackend stores and retrieves dependency-cache archives.
// Implementations share the tar.gz format, which is what keeps a
// laptop run and a cluster run interchangeable.
type depCacheBackend interface {
	label() string
	exists(ctx context.Context, key string) (bool, error)
	// fetch extracts the archive stored under key into dir,
	// returning the archive's size in bytes.
	fetch(ctx context.Context, key, dir string) (int64, error)
	// store archives dir under key, returning the archive's size.
	store(ctx context.Context, key, dir string) (int64, error)
}

// selectDepCacheBackend picks remote when a cache service URL is in
// the environment (SPARKWING_CACHE_URL as set for runner pods, or
// SPARKWING_GITCACHE_URL as set for warm runners), local otherwise.
func selectDepCacheBackend() depCacheBackend {
	if url := strings.TrimRight(os.Getenv("SPARKWING_CACHE_URL"), "/"); url != "" {
		return &remoteDepCache{baseURL: url, token: depCacheToken()}
	}
	if url := strings.TrimRight(os.Getenv("SPARKWING_GITCACHE_URL"), "/"); url != "" {
		return &remoteDepCache{baseURL: url, token: depCacheToken()}
	}
	return &localDepCache{}
}

// depCacheToken resolves the bearer for the cache service:
// SPARKWING_CACHE_TOKEN (the bincache convention), then
// SPARKWING_AGENT_TOKEN (what runner pods carry).
func depCacheToken() string {
	if t := os.Getenv("SPARKWING_CACHE_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("SPARKWING_AGENT_TOKEN")
}

// localDepCache stores archives under $SPARKWING_HOME/depcache.
type localDepCache struct{}

func (l *localDepCache) label() string { return "local" }

func (l *localDepCache) archivePath(key string) (string, error) {
	p, err := paths.DefaultPaths()
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Root, "depcache", key+".tar.gz"), nil
}

func (l *localDepCache) exists(_ context.Context, key string) (bool, error) {
	p, err := l.archivePath(key)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (l *localDepCache) fetch(_ context.Context, key, dir string) (int64, error) {
	p, err := l.archivePath(key)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if err := extractDepCacheArchiveStaged(f, dir); err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (l *localDepCache) store(_ context.Context, key, dir string) (int64, error) {
	p, err := l.archivePath(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return 0, err
	}
	// safety: temp-then-rename keeps a concurrent reader off a
	// half-written archive.
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+key+"-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := writeDepCacheArchive(tmp, dir); err != nil {
		tmp.Close()
		return 0, err
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if err := os.Rename(tmpPath, p); err != nil {
		return 0, err
	}
	return size, nil
}

// remoteDepCache speaks the cache service's /cache/<key> blob
// protocol: HEAD probes, GET fetches, PUT stores.
type remoteDepCache struct {
	baseURL string
	token   string
}

func (r *remoteDepCache) label() string { return "cluster" }

func (r *remoteDepCache) client() *http.Client {
	return &http.Client{Timeout: depCacheHTTPTimeout}
}

func (r *remoteDepCache) newRequest(ctx context.Context, method, key string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.baseURL+"/cache/"+key, body)
	if err != nil {
		return nil, err
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	return req, nil
}

func (r *remoteDepCache) exists(ctx context.Context, key string) (bool, error) {
	req, err := r.newRequest(ctx, http.MethodHead, key, nil)
	if err != nil {
		return false, err
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("HEAD /cache/%s: %s", key, resp.Status)
	}
}

func (r *remoteDepCache) fetch(ctx context.Context, key, dir string) (int64, error) {
	req, err := r.newRequest(ctx, http.MethodGet, key, nil)
	if err != nil {
		return 0, err
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return 0, fmt.Errorf("GET /cache/%s: %s", key, resp.Status)
	}
	counted := &countingReader{r: resp.Body}
	if err := extractDepCacheArchiveStaged(counted, dir); err != nil {
		return 0, err
	}
	return counted.n, nil
}

func (r *remoteDepCache) store(ctx context.Context, key, dir string) (int64, error) {
	// safety: the service bounds PUT bodies; archiving to a temp file
	// first makes oversize a cheap client-side skip, not a mid-flight
	// failure.
	tmp, err := os.CreateTemp("", "sparkwing-depcache-*.tar.gz")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := writeDepCacheArchive(tmp, dir); err != nil {
		tmp.Close()
		return 0, err
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if size > remoteDepCacheMaxBytes {
		return 0, fmt.Errorf("archive is %s, over the cache service's %s limit; not uploading",
			humanBytes(size), humanBytes(remoteDepCacheMaxBytes))
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	req, err := r.newRequest(ctx, http.MethodPut, key, f)
	if err != nil {
		return 0, err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/gzip")

	resp, err := r.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("PUT /cache/%s: %s", key, resp.Status)
	}
	return size, nil
}

// countingReader counts bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
