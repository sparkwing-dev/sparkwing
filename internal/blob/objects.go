package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func validateObject(o capability.ObjectToSign, now time.Time) error {
	if o.Size < 0 || !validDigest(Digest(o.SHA256)) || !o.ExpiresAt.After(now) {
		return fmt.Errorf("invalid signed object size, digest or expiry")
	}
	parts := strings.Split(o.Key, "/")
	if len(parts) != 4 || parts[0] != "teams" {
		return fmt.Errorf("invalid signed object key")
	}
	final, err := ObjectKey(parts[1], Digest(o.SHA256))
	if err != nil {
		return err
	}
	switch o.Method {
	case http.MethodGet:
		if o.Key == final {
			if lifetime := o.ExpiresAt.Sub(now); lifetime > limits.DataDownloadURLTTL {
				return fmt.Errorf("data download lifetime %s exceeds %s", lifetime, limits.DataDownloadURLTTL)
			}
			return nil
		}
	case http.MethodPut:
		return validatePending(o.Key, parts[1])
	}
	return fmt.Errorf("invalid signed object method or key")
}

func validatePending(key, team string) error {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "teams" || parts[1] != team || parts[2] != "pending" || parts[3] == "" {
		return fmt.Errorf("invalid pending object key")
	}
	// safety: ticket IDs are single filesystem and URL components on every platform.
	for _, c := range parts[3] {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return fmt.Errorf("invalid pending object ticket")
		}
	}
	return nil
}

func validateDeleteKey(key string) error {
	parts := strings.Split(key, "/")
	if len(parts) != 4 {
		return fmt.Errorf("invalid object key")
	}
	if parts[2] == "cas" {
		want, err := ObjectKey(parts[1], Digest(parts[3]))
		if err == nil && want == key {
			return nil
		}
		return fmt.Errorf("invalid CAS object key")
	}
	if _, err := ObjectKey(parts[1], Digest(strings.Repeat("0", 64))); err != nil {
		return err
	}
	return validatePending(key, parts[1])
}

func verifyObject(ctx context.Context, body io.ReadCloser, expected Blob) error {
	r := newVerifiedReader(ctx, body, SignedTransfer{Digest: expected.Digest, Size: expected.Size})
	_, err := io.Copy(io.Discard, r)
	err = errors.Join(err, r.Close(), ctx.Err())
	if err != nil {
		return err
	}
	if !r.Verified() {
		return ErrIntegrity
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

// VerifyBlob verifies the selected immutable CAS object before result reuse.
func (f *Filesystem) VerifyBlob(ctx context.Context, expected Blob) error {
	key, err := ObjectKey(expected.Team, expected.Digest)
	if err != nil {
		return err
	}
	return f.verify(ctx, key, expected)
}

// VerifyBlob verifies the selected immutable CAS object before result reuse.
func (s *S3) VerifyBlob(ctx context.Context, expected Blob) error {
	key, err := ObjectKey(expected.Team, expected.Digest)
	if err != nil {
		return err
	}
	_, err = s.verify(ctx, key, expected)
	return err
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
