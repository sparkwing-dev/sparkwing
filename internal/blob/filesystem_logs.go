package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
)

// SignLogPut signs a PUT of exact bytes to one server-derived log key. Writer
// authority and the run's retention lifecycle must be checked before calling it.
func (f *Filesystem) SignLogPut(ctx context.Context, key string, size int64, digest Digest, expiry time.Time) (string, map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	object := capability.ObjectToSign{Method: http.MethodPut, Key: key, Size: size, SHA256: string(digest), ExpiresAt: expiry}
	if err := validateFilesystemLog(object, f.now()); err != nil {
		return "", nil, err
	}
	signed, err := f.signObject(object)
	return signed.URL, signed.Headers, err
}

func logObjectLimit(key string) (int64, error) {
	p := strings.Split(key, "/")
	if !fs.ValidPath(key) || len(p) < 8 || p[0] != "logs" || p[2] != "teams" || p[4] != "runs" || strings.ContainsAny(key, "\\:%") || strings.ContainsFunc(key, unicode.IsControl) {
		return 0, fmt.Errorf("invalid log object key")
	}
	for _, part := range p {
		if strings.HasPrefix(part, ".") {
			return 0, fmt.Errorf("invalid log object key")
		}
	}
	if _, ok := LogClassDays(p[1]); !ok {
		return 0, fmt.Errorf("invalid log retention class")
	}
	name := p[len(p)-1]
	if name == "manifest.json" {
		return limits.LogManifestBytes, nil
	}
	if _, ok := LogSegmentNumber("", name); !ok {
		return 0, fmt.Errorf("invalid log segment number")
	}
	return limits.LogSegmentUploadBytes, nil
}

func validLogPayload(key string, size int64, digest Digest) error {
	limit, err := logObjectLimit(key)
	if err != nil {
		return err
	}
	if size < 1 || size > limit || !validDigest(digest) {
		return fmt.Errorf("invalid log upload size or digest")
	}
	return nil
}

func validateFilesystemLog(object capability.ObjectToSign, now time.Time) error {
	if err := validLogPayload(object.Key, object.Size, Digest(object.SHA256)); err != nil {
		return err
	}
	if object.Method != http.MethodPut || !object.ExpiresAt.After(now) || object.ExpiresAt.Sub(now) > limits.LogUploadURLTTL {
		return fmt.Errorf("invalid signed log PUT")
	}
	return nil
}

// Get opens a log object for an authorized controller read. It grants no signed
// download URL; the controller verifies committed ranges and meters its response.
func (f *Filesystem) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := logObjectLimit(key); err != nil {
		return nil, err
	}
	file, err := f.root.Open(key)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("log object is not a regular file"), err, file.Close())
	}
	return file, nil
}
