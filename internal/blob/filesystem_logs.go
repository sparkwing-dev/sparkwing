package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// SignLogPut signs a bounded PUT to one server-derived log key. Writer authority
// and the run's retention lifecycle must be checked before calling it. Unlike
// data uploads, future batch slots have no known digest or length at signing.
func (f *Filesystem) SignLogPut(ctx context.Context, key string, expiry time.Time) (string, map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	limit, err := logObjectLimit(key)
	if err != nil {
		return "", nil, err
	}
	object := capability.ObjectToSign{Method: http.MethodPut, Key: key, Size: limit, ExpiresAt: expiry}
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
	digits, ok := strings.CutPrefix(name, "seg-")
	if !ok {
		return 0, fmt.Errorf("invalid log object name")
	}
	digits, ok = strings.CutSuffix(digits, ".ndjson.gz")
	n, err := strconv.Atoi(digits)
	if !ok || err != nil || n < 1 || n > limits.MaxLogSegmentNumber || fmt.Sprintf("%06d", n) != digits {
		return 0, fmt.Errorf("invalid log segment number")
	}
	return limits.LogSegmentUploadBytes, nil
}

func validateFilesystemLog(object capability.ObjectToSign, now time.Time) error {
	limit, err := logObjectLimit(object.Key)
	if err != nil {
		return err
	}
	if object.Method != http.MethodPut || object.SHA256 != "" || object.Size != limit || !object.ExpiresAt.After(now) || object.ExpiresAt.Sub(now) > limits.LogUploadURLTTL {
		return fmt.Errorf("invalid signed log PUT")
	}
	return nil
}

func (f *Filesystem) putLog(r *http.Request, object capability.ObjectToSign) error {
	if r.ContentLength > object.Size {
		return ErrIntegrity
	}
	raw, err := io.ReadAll(io.LimitReader(&contextReader{ctx: r.Context(), reader: r.Body}, object.Size+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > object.Size || (r.ContentLength >= 0 && r.ContentLength != int64(len(raw))) {
		return ErrIntegrity
	}
	sum := sha256.Sum256(raw)
	object.Size, object.SHA256 = int64(len(raw)), hex.EncodeToString(sum[:])
	upload := r.Clone(r.Context())
	upload.Body, upload.ContentLength = io.NopCloser(bytes.NewReader(raw)), object.Size
	// safety: the CAS writer links once and verifies an existing object, so a
	// retried signed slot cannot replace a committed segment or final manifest.
	return f.put(upload, object)
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
