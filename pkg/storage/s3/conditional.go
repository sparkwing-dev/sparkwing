package s3

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// casProbe memoizes the result of ConditionalWritesSupported so the
// live-endpoint probe runs at most once per store.
type casProbe struct {
	mu       sync.Mutex
	resolved bool
	ok       bool
	err      error
}

var _ storage.ConditionalWriter = (*ArtifactStore)(nil)

// GetWithETag returns the object and its current ETag. The ETag feeds
// back into PutIfMatch to gate the next write.
func (s *ArtifactStore) GetWithETag(ctx context.Context, key string) (io.ReadCloser, storage.ETag, error) {
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(s.artifactKey(key)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, "", storage.ErrNotFound
		}
		return nil, "", fmt.Errorf("s3 get %s: %w", key, err)
	}
	return out.Body, etagOf(out.ETag), nil
}

// PutIfAbsent writes only when key has no object, using If-None-Match:
// *. A pre-existing object yields ErrPreconditionFailed.
func (s *ArtifactStore) PutIfAbsent(ctx context.Context, key string, r io.Reader) (storage.ETag, error) {
	out, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.Bucket),
		Key:         aws.String(s.artifactKey(key)),
		Body:        r,
		IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		if isPreconditionFailed(err) {
			return "", storage.ErrPreconditionFailed
		}
		return "", fmt.Errorf("s3 put-if-absent %s: %w", key, err)
	}
	return etagOf(out.ETag), nil
}

// PutIfMatch writes only when the current ETag equals expect, using
// If-Match. A differing or absent object yields ErrPreconditionFailed.
func (s *ArtifactStore) PutIfMatch(ctx context.Context, key string, r io.Reader, expect storage.ETag) (storage.ETag, error) {
	out, err := s.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:  aws.String(s.Bucket),
		Key:     aws.String(s.artifactKey(key)),
		Body:    r,
		IfMatch: aws.String(string(expect)),
	})
	if err != nil {
		if isPreconditionFailed(err) {
			return "", storage.ErrPreconditionFailed
		}
		return "", fmt.Errorf("s3 put-if-match %s: %w", key, err)
	}
	return etagOf(out.ETag), nil
}

// ConditionalWritesSupported probes the live endpoint once and
// memoizes. The probe does a create-if-absent on a fresh key, then a
// second create-if-absent on the same key: an endpoint that enforces
// preconditions rejects the second with ErrPreconditionFailed, while
// one that ignores them accepts it. The probe key is deleted after.
func (s *ArtifactStore) ConditionalWritesSupported(ctx context.Context) (bool, error) {
	s.casOnce.mu.Lock()
	defer s.casOnce.mu.Unlock()
	if s.casOnce.resolved {
		return s.casOnce.ok, s.casOnce.err
	}
	ok, err := s.probeConditionalWrites(ctx)
	s.casOnce.resolved = true
	s.casOnce.ok = ok
	s.casOnce.err = err
	return ok, err
}

func (s *ArtifactStore) probeConditionalWrites(ctx context.Context) (bool, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return false, fmt.Errorf("cas probe nonce: %w", err)
	}
	key := ".sparkwing-cas-probe/" + hex.EncodeToString(nonce)
	defer func() { _ = s.Delete(context.WithoutCancel(ctx), key) }()

	if _, err := s.PutIfAbsent(ctx, key, emptyReader()); err != nil {
		return false, fmt.Errorf("cas probe initial write: %w", err)
	}
	_, err := s.PutIfAbsent(ctx, key, emptyReader())
	if errors.Is(err, storage.ErrPreconditionFailed) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("cas probe second write: %w", err)
	}
	// The endpoint accepted a create-if-absent over an existing key:
	// it ignores preconditions. Fall back to last-write-wins.
	return false, nil
}

// etagOf normalizes a possibly-nil SDK ETag pointer.
func etagOf(p *string) storage.ETag {
	if p == nil {
		return ""
	}
	return storage.ETag(*p)
}

// isPreconditionFailed matches S3's 412 PreconditionFailed and the 409
// ConditionalRequestConflict that a concurrent conditional write can
// raise; both mean "re-read and retry the CAS."
func isPreconditionFailed(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "PreconditionFailed", "ConditionalRequestConflict", "412", "409":
			return true
		}
	}
	return false
}

func emptyReader() io.Reader { return strings.NewReader("") }
