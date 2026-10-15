package logseg

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// LogPutSigner issues PUT-only URLs and their required headers to server-derived
// log keys. Existing bytes must be preserved. A precondition failure grants no
// proof that the object matches the retried payload.
type LogPutSigner interface {
	SignLogPut(context.Context, string, time.Time) (string, map[string]string, error)
}

// BatchSigner assembles storage URLs after controller authorization. ClassReady
// checks the backend lifecycle rule; the caller supplies the run's persisted
// retention class. Now defaults to time.Now.
type BatchSigner struct {
	Objects    LogPutSigner
	ClassReady func(context.Context, string) error
	Now        func() time.Time
}

// Sign returns dense immutable segment slots and one final-manifest slot. It
// neither authenticates a writer nor selects retention from current standing.
func (s BatchSigner) Sign(ctx context.Context, request blob.LogSigningRequest, class string) (blob.LogUploadBatch, error) {
	if err := ctx.Err(); err != nil {
		return blob.LogUploadBatch{}, err
	}
	if s.Objects == nil || s.ClassReady == nil {
		return blob.LogUploadBatch{}, fmt.Errorf("logseg: batch signing requires objects and lifecycle validation")
	}
	if request.FirstSegment < 1 || request.FirstSegment > int64(limits.MaxLogSegmentNumber) || request.Count < 1 || request.Count > limits.LogUploadBatch || int64(request.Count)-1 > int64(limits.MaxLogSegmentNumber)-request.FirstSegment {
		return blob.LogUploadBatch{}, fmt.Errorf("logseg: invalid segment signing range")
	}
	subject := cloneSubject(request.Subject)
	prefix, err := SubjectPrefix(class, subject)
	if err != nil {
		return blob.LogUploadBatch{}, err
	}
	if err := s.ClassReady(ctx, class); err != nil {
		return blob.LogUploadBatch{}, err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	batch := blob.LogUploadBatch{Subject: subject, RetentionClass: class, ExpiresAt: now().Add(limits.LogUploadURLTTL)}
	for i := range request.Count {
		if err := ctx.Err(); err != nil {
			return blob.LogUploadBatch{}, err
		}
		index := request.FirstSegment + int64(i)
		u, headers, err := s.Objects.SignLogPut(ctx, SegmentKey(prefix, int(index)), batch.ExpiresAt)
		if err != nil {
			return blob.LogUploadBatch{}, err
		}
		batch.Segments = append(batch.Segments, blob.LogUploadURL{Index: index, URL: u, Headers: headers, ExpiresAt: batch.ExpiresAt})
	}
	batch.ManifestURL, batch.ManifestHeaders, err = s.Objects.SignLogPut(ctx, ManifestKey(prefix), batch.ExpiresAt)
	if err != nil {
		return blob.LogUploadBatch{}, err
	}
	return batch, nil
}
