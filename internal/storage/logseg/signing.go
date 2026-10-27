package logseg

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// LogPutSigner issues PUT-only URLs bound to exact bytes and their required
// headers. Existing bytes must be preserved. A precondition failure grants no
// proof that the object matches the retried payload.
type LogPutSigner interface {
	SignLogPut(ctx context.Context, key string, size int64, digest blob.Digest, expiry time.Time) (string, map[string]string, error)
}

// Signer assembles one storage URL after controller authorization. ClassReady
// checks the backend lifecycle rule; the caller supplies the run's persisted
// retention class. Now defaults to time.Now. Expires, when set, is the slot's
// fixed upload deadline; otherwise the URL lasts limits.LogUploadURLTTL.
type Signer struct {
	Objects    LogPutSigner
	ClassReady func(context.Context, string) error
	Now        func() time.Time
	Expires    time.Time
}

// Sign returns one immutable segment or final-manifest slot for the requested
// bytes. It neither authenticates a writer nor selects retention from current standing.
func (s Signer) Sign(ctx context.Context, request blob.LogSigningRequest, class string) (blob.LogUploadURL, error) {
	if err := ctx.Err(); err != nil {
		return blob.LogUploadURL{}, err
	}
	if s.Objects == nil || s.ClassReady == nil {
		return blob.LogUploadURL{}, fmt.Errorf("logseg: signing requires objects and lifecycle validation")
	}
	if request.Index < 0 || request.Index > int64(limits.MaxLogSegmentNumber) {
		return blob.LogUploadURL{}, fmt.Errorf("logseg: invalid segment index")
	}
	subject := cloneSubject(request.Subject)
	prefix, err := blob.LogSubjectPrefix(class, subject)
	if err != nil {
		return blob.LogUploadURL{}, err
	}
	if err := s.ClassReady(ctx, class); err != nil {
		return blob.LogUploadURL{}, err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	key := blob.LogManifestKey(prefix)
	if request.Index > 0 {
		key = blob.LogSegmentKey(prefix, int(request.Index))
	}
	slot := blob.LogUploadURL{Subject: subject, RetentionClass: class, Index: request.Index, ExpiresAt: now().Add(limits.LogUploadURLTTL)}
	if !s.Expires.IsZero() {
		slot.ExpiresAt = s.Expires
	}
	slot.URL, slot.Headers, err = s.Objects.SignLogPut(ctx, key, request.Size, request.Digest, slot.ExpiresAt)
	if err != nil {
		return blob.LogUploadURL{}, err
	}
	return slot, nil
}
