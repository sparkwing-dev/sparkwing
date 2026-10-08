package depcache

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/tarsafe"
)

// safety: a client-side skip, not the server's cap. It saves uploading an
// archive the cache service would refuse, and the service's own
// --max-cache-archive-bytes is the bound that actually holds; an operator who
// lowers that will see refusals here rather than skips.
var remoteMaxBytes = int64(500 << 20)

const httpTimeout = 10 * time.Minute

type backend interface {
	label() string
	exists(ctx context.Context, key string) (bool, error)

	fetch(ctx context.Context, key, dir string) (int64, error)

	store(ctx context.Context, key, dir string) (int64, error)
}

func selectBackend() backend {
	if url := strings.TrimRight(os.Getenv("SPARKWING_CACHE_URL"), "/"); url != "" {
		return &remoteBackend{baseURL: url, token: cacheToken()}
	}
	if url := strings.TrimRight(os.Getenv("SPARKWING_GITCACHE_URL"), "/"); url != "" {
		return &remoteBackend{baseURL: url, token: cacheToken()}
	}
	return &localBackend{}
}

func cacheToken() string {
	if t := authwire.CacheBearerFromEnv(); t != "" {
		return t
	}
	return os.Getenv("SPARKWING_AGENT_TOKEN")
}

type localBackend struct{}

func (l *localBackend) label() string { return "local" }

func (l *localBackend) archivePath(key string) (string, error) {
	p, err := paths.DefaultPaths()
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Root, "depcache", key+".tar.gz"), nil
}

func (l *localBackend) exists(_ context.Context, key string) (bool, error) {
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

func (l *localBackend) fetch(_ context.Context, key, dir string) (int64, error) {
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
	if err := extractArchiveStaged(f, dir); err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (l *localBackend) store(_ context.Context, key, dir string) (int64, error) {
	p, err := l.archivePath(key)
	if err != nil {
		return 0, err
	}
	if err := fssecure.EnsureDir(filepath.Dir(p)); err != nil {
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

	if err := writeArchive(tmp, dir); err != nil {
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

type remoteBackend struct {
	baseURL string
	token   string
}

func (r *remoteBackend) label() string { return "cluster" }

func (r *remoteBackend) client() *http.Client {
	return &http.Client{Timeout: httpTimeout}
}

func (r *remoteBackend) newRequest(ctx context.Context, method, key string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.baseURL+"/cache/"+key, body)
	if err != nil {
		return nil, err
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	return req, nil
}

func (r *remoteBackend) exists(ctx context.Context, key string) (bool, error) {
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

func (r *remoteBackend) fetch(ctx context.Context, key, dir string) (int64, error) {
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
	if err := extractArchiveStaged(counted, dir); err != nil {
		return 0, err
	}
	return counted.n, nil
}

func (r *remoteBackend) store(ctx context.Context, key, dir string) (int64, error) {
	// safety: the service bounds PUT bodies; archiving to a temp file
	// first makes oversize a cheap client-side skip, not a mid-flight
	// failure.
	tmp, err := os.CreateTemp("", "sparkwing-depcache-*.tar.gz")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := writeArchive(tmp, dir); err != nil {
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
	if size > remoteMaxBytes {
		return 0, fmt.Errorf("archive is %s, over the %s this client uploads; not uploading. "+
			"The cache service's own limit is --max-cache-archive-bytes",
			tarsafe.HumanBytes(size), tarsafe.HumanBytes(remoteMaxBytes))
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

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
