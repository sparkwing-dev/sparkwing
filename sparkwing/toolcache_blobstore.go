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

const lintCacheManifestName = "workdir"

// ErrLintCacheWorkdirMismatch prevents restoring paths recorded for another workdir.
var ErrLintCacheWorkdirMismatch = errors.New("lint cache archive was produced at a different workdir")

// LintCacheBlobKey returns the /cache/<key> identifier for the
// golangci-lint tool cache for the current WorkDir.
func LintCacheBlobKey() string {
	return lintCacheKey(WorkDir())
}

func lintCacheKey(workdir string) string {
	sum := sha256.Sum256([]byte("golangci-lint\x00" + workdir))
	return "lint-cache-" + hex.EncodeToString(sum[:8])
}

// SaveLintCache compresses the golangci-lint tool-cache directory for
// the current WorkDir and PUTs it to gcURL/cache/<key>.
// An empty URL or missing or empty cache returns zero bytes without a request.
func SaveLintCache(ctx context.Context, gcURL, token string) (sent int64, resultErr error) {
	if gcURL == "" {
		return 0, nil
	}
	cacheDirectory := ToolCacheDir("golangci-lint")
	empty, err := isDirEmpty(cacheDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("save lint cache: inspect directory: %w", err)
	}
	if empty {
		return 0, nil
	}

	archiveFile, err := os.CreateTemp("", "sparkwing-lintcache-*.tar.gz")
	if err != nil {
		return 0, fmt.Errorf("save lint cache: create temp: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, archiveFile.Close(), os.Remove(archiveFile.Name())) }()
	if err := writeLintCacheArchive(archiveFile, cacheDirectory, WorkDir()); err != nil {
		return 0, fmt.Errorf("save lint cache: archive: %w", err)
	}
	size, err := archiveFile.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, fmt.Errorf("save lint cache: seek: %w", err)
	}

	url := strings.TrimRight(gcURL, "/") + "/cache/" + LintCacheBlobKey()
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, url, io.NewSectionReader(archiveFile, 0, size))
	if err != nil {
		return 0, err
	}
	request.ContentLength = size
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/gzip")

	client := &http.Client{Timeout: 10 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("save lint cache: PUT: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()
	_, readErr := io.Copy(io.Discard, response.Body)

	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return 0, errors.Join(fmt.Errorf("save lint cache: server returned %s", response.Status), readErr)
	}
	if readErr != nil {
		return 0, fmt.Errorf("save lint cache: read response: %w", readErr)
	}
	return size, nil
}

// RestoreLintCache downloads the blob-store seed for the current WorkDir
// and expands it into the golangci-lint tool-cache directory.
// An empty URL or HTTP 404 returns (false, 0, nil). I/O failures and archives
// with another workdir return errors. SPARKWING_CACHE_TOKEN authenticates reads.
func RestoreLintCache(ctx context.Context, gcURL string) (restored bool, received int64, resultErr error) {
	if gcURL == "" {
		return false, 0, nil
	}

	url := strings.TrimRight(gcURL, "/") + "/cache/" + LintCacheBlobKey()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, 0, err
	}
	if token := os.Getenv("SPARKWING_CACHE_TOKEN"); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return false, 0, fmt.Errorf("restore lint cache: GET: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()

	if response.StatusCode == http.StatusNotFound {
		return false, 0, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, 0, fmt.Errorf("restore lint cache: server returned %s", response.Status)
	}

	counted := &countReader{reader: response.Body}
	cacheDirectory := ToolCacheDir("golangci-lint")
	if err := extractLintCacheArchiveStaged(counted, cacheDirectory, WorkDir()); err != nil {
		return false, counted.bytesRead, err
	}
	return true, counted.bytesRead, nil
}

func writeLintCacheArchive(writer io.Writer, cacheDirectory, workdir string) (resultErr error) {
	compressed := gzip.NewWriter(writer)
	archiveWriter := tar.NewWriter(compressed)
	defer func() { resultErr = errors.Join(resultErr, archiveWriter.Close(), compressed.Close()) }()

	manifest := []byte(workdir)
	if err := archiveWriter.WriteHeader(&tar.Header{
		Name: lintCacheManifestName,
		Mode: 0o600,
		Size: int64(len(manifest)),
	}); err != nil {
		return err
	}
	if _, err := archiveWriter.Write(manifest); err != nil {
		return err
	}

	if err := filepath.WalkDir(cacheDirectory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(cacheDirectory, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = "cache/" + filepath.ToSlash(relative)
		if entry.IsDir() {
			header.Name += "/"
		} else if !info.Mode().IsRegular() {
			return nil
		}
		if err := archiveWriter.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		// #nosec G122 -- a TOCTOU swap here needs write access to this user's own tool cache, so the race is accepted
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(archiveWriter, file)
		return errors.Join(copyErr, file.Close())
	}); err != nil {
		return err
	}

	return nil
}

func extractLintCacheArchiveStaged(reader io.Reader, destination, runningWorkdir string) error {
	return extractIntoDirStaged(destination, ".lintcache-restore-*", func(stage string) error {
		return extractLintCacheArchive(reader, stage, runningWorkdir)
	})
}

func extractLintCacheArchive(reader io.Reader, destination, runningWorkdir string) (resultErr error) {
	compressed, err := gzip.NewReader(reader)
	if err != nil {
		return fmt.Errorf("extract lint cache: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, compressed.Close()) }()

	archiveReader := tar.NewReader(compressed)

	// SAFETY: Validate the archive workdir before creating destination files.
	header, err := archiveReader.Next()
	if err != nil {
		return fmt.Errorf("extract lint cache: read manifest: %w", err)
	}
	if header.Name != lintCacheManifestName {
		return fmt.Errorf("extract lint cache: unexpected first entry %q", header.Name)
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(archiveReader, 4096))
	if err != nil {
		return fmt.Errorf("extract lint cache: read manifest body: %w", err)
	}
	archiveWorkdir := string(manifestBytes)
	if archiveWorkdir != runningWorkdir {
		return fmt.Errorf("%w: archive=%q running=%q", ErrLintCacheWorkdirMismatch, archiveWorkdir, runningWorkdir)
	}

	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}

	if err := extractTarInRoot(archiveReader, destination, tarExtractPolicy{
		minDirPerm:  0o700,
		minFilePerm: 0o600,
		rename:      lintCacheEntryName,
	}); err != nil {
		return fmt.Errorf("extract lint cache: %w", err)
	}
	return nil
}

func lintCacheEntryName(name string) (string, bool) {
	relative, ok := strings.CutPrefix(name, "cache/")
	if !ok || relative == "" {
		return "", false
	}
	return relative, true
}

func isDirEmpty(directory string) (empty bool, resultErr error) {
	file, err := os.Open(directory)
	if err != nil {
		return true, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	_, err = file.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err
}

type countReader struct {
	reader    io.Reader
	bytesRead int64
}

func (counter *countReader) Read(buffer []byte) (int, error) {
	bytesRead, err := counter.reader.Read(buffer)
	counter.bytesRead += int64(bytesRead)
	return bytesRead, err
}
