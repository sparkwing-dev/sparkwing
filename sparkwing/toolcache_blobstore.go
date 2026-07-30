package sparkwing

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// lintCacheManifestName is the first entry in the lint-cache archive.
// It holds the absolute workdir path so RestoreLintCache can verify the
// cache was produced at the same path before writing any files.
const lintCacheManifestName = "workdir"

// ErrLintCacheWorkdirMismatch is returned when the archive's recorded
// workdir does not match the running WorkDir. The cache is structurally
// valid, but restoring it would replay another tree's file paths.
var ErrLintCacheWorkdirMismatch = errors.New("lint cache archive was produced at a different workdir")

// LintCacheBlobKey returns the /cache/<key> identifier for the
// golangci-lint tool cache for the current WorkDir. Two runners at the
// same absolute path produce the same key; runners at different absolute
// paths produce different keys, so the blob store cannot serve a cache
// across a path boundary.
func LintCacheBlobKey() string {
	return lintCacheKey(WorkDir())
}

func lintCacheKey(workdir string) string {
	sum := sha256.Sum256([]byte("golangci-lint\x00" + workdir))
	return "lint-cache-" + hex.EncodeToString(sum[:8])
}

// SaveLintCache compresses the golangci-lint tool-cache directory for
// the current WorkDir and PUTs it to gcURL/cache/<key>. Returns the
// number of bytes sent to the server on success. An empty gcURL or an
// empty (or missing) tool-cache directory is a no-op that returns (0, nil).
//
// The archive embeds the current WorkDir as its first entry so
// RestoreLintCache can detect a workdir mismatch before expanding any files.
func SaveLintCache(ctx context.Context, gcURL, token string) (int64, error) {
	if gcURL == "" {
		return 0, nil
	}
	cacheDir := ToolCacheDir("golangci-lint")
	if empty, _ := isDirEmpty(cacheDir); empty {
		return 0, nil
	}

	tmp, err := os.CreateTemp("", "sparkwing-lintcache-*.tar.gz")
	if err != nil {
		return 0, fmt.Errorf("save lint cache: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := writeLintCacheArchive(tmp, cacheDir, WorkDir()); err != nil {
		tmp.Close()
		return 0, fmt.Errorf("save lint cache: archive: %w", err)
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		tmp.Close()
		return 0, fmt.Errorf("save lint cache: seek: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("save lint cache: close temp: %w", err)
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return 0, fmt.Errorf("save lint cache: reopen temp: %w", err)
	}
	defer f.Close()

	url := strings.TrimRight(gcURL, "/") + "/cache/" + LintCacheBlobKey()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, f)
	if err != nil {
		return 0, err
	}
	req.ContentLength = size
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/gzip")

	cli := &http.Client{Timeout: 10 * time.Minute}
	resp, err := cli.Do(req)
	if err != nil {
		return 0, fmt.Errorf("save lint cache: PUT: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("save lint cache: server returned %s", resp.Status)
	}
	return size, nil
}

// RestoreLintCache downloads the blob-store seed for the current WorkDir
// and expands it into the golangci-lint tool-cache directory. Returns
// (true, bytes, nil) on a hit, (false, 0, nil) when no seed exists yet
// (404), and an error for I/O failures.
//
// If the archive records a different WorkDir than the running one,
// RestoreLintCache returns ErrLintCacheWorkdirMismatch rather than
// silently expanding paths from another tree.
func RestoreLintCache(ctx context.Context, gcURL string) (bool, int64, error) {
	if gcURL == "" {
		return false, 0, nil
	}

	url := strings.TrimRight(gcURL, "/") + "/cache/" + LintCacheBlobKey()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, 0, err
	}

	cli := &http.Client{Timeout: 10 * time.Minute}
	resp, err := cli.Do(req)
	if err != nil {
		return false, 0, fmt.Errorf("restore lint cache: GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, 0, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, 0, fmt.Errorf("restore lint cache: server returned %s", resp.Status)
	}

	counted := &countReader{r: resp.Body}
	cacheDir := ToolCacheDir("golangci-lint")
	if err := extractLintCacheArchive(counted, cacheDir, WorkDir()); err != nil {
		return false, counted.n, err
	}
	return true, counted.n, nil
}

// writeLintCacheArchive streams a gzipped tar to w. The first entry is a
// workdir manifest; remaining entries hold the cache tree under "cache/".
func writeLintCacheArchive(w io.Writer, cacheDir, workdir string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	manifest := []byte(workdir)
	if err := tw.WriteHeader(&tar.Header{
		Name: lintCacheManifestName,
		Mode: 0o600,
		Size: int64(len(manifest)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(manifest); err != nil {
		return err
	}

	if err := filepath.WalkDir(cacheDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(cacheDir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = "cache/" + filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		} else if !info.Mode().IsRegular() {
			return nil
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	}); err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// extractLintCacheArchive reads a lint-cache archive from r, verifies
// the embedded workdir matches runningWorkdir, then expands the cache
// files into destDir. Each regular file is written to a temp path and
// renamed into place so a partial restore does not leave half-written files.
func extractLintCacheArchive(r io.Reader, destDir, runningWorkdir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("extract lint cache: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	// safety: first entry must be the manifest; check before touching destDir
	hdr, err := tr.Next()
	if err != nil {
		return fmt.Errorf("extract lint cache: read manifest: %w", err)
	}
	if hdr.Name != lintCacheManifestName {
		return fmt.Errorf("extract lint cache: unexpected first entry %q", hdr.Name)
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(tr, 4096))
	if err != nil {
		return fmt.Errorf("extract lint cache: read manifest body: %w", err)
	}
	archiveWorkdir := string(manifestBytes)
	if archiveWorkdir != runningWorkdir {
		return fmt.Errorf("%w: archive=%q running=%q", ErrLintCacheWorkdirMismatch, archiveWorkdir, runningWorkdir)
	}

	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("extract lint cache: %w", err)
		}

		name := hdr.Name
		if !strings.HasPrefix(name, "cache/") {
			continue
		}
		rel := strings.TrimPrefix(name, "cache/")
		if rel == "" || rel == "." {
			continue
		}

		target := filepath.Join(destDir, filepath.FromSlash(rel))
		// safety: reject path-traversal entries
		if !strings.HasPrefix(target+string(os.PathSeparator), destDir+string(os.PathSeparator)) {
			continue
		}

		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, fs.FileMode(hdr.Mode)&0o777|0o700); err != nil {
				return err
			}
			continue
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		tmp := target + ".tmp"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(hdr.Mode)&0o777|0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(f, tr)
		closeErr := f.Close()
		if copyErr != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("extract lint cache: write %s: %w", rel, copyErr)
		}
		if closeErr != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("extract lint cache: close %s: %w", rel, closeErr)
		}
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	return nil
}

func isDirEmpty(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return true, err
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
