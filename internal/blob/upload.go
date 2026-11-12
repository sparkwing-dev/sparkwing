package blob

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// Upload retries replayable PUTs at most three times, refreshing expired signing authority through renew.
// Non-seekable bodies get one attempt; the caller retains ownership of the reader.
func Upload(ctx context.Context, client HTTPClient, signed SignedTransfer, body io.Reader, renew func(context.Context) (SignedTransfer, error)) (io.ReadCloser, error) {
	if body == nil {
		return nil, errors.New("signed PUT requires an upload body")
	}
	seeker, replayable := body.(io.ReadSeeker)
	var start int64
	if replayable {
		var err error
		start, err = seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
	}
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !signed.ExpiresAt.IsZero() && !signed.ExpiresAt.After(time.Now()) {
			if renew == nil {
				return nil, errors.New("signed PUT ticket expired before transfer")
			}
			var err error
			signed, err = renew(ctx)
			if err != nil {
				return nil, err
			}
			if !signed.ExpiresAt.IsZero() && !signed.ExpiresAt.After(time.Now()) {
				return nil, errors.New("renewed signed PUT ticket is already expired")
			}
		}
		if replayable {
			if _, err := seeker.Seek(start, io.SeekStart); err != nil {
				return nil, err
			}
		}
		var requestBody io.ReadCloser = io.NopCloser(body)
		if replayable {
			requestBody = &uploadBody{reader: body}
		}
		response, err := client.Transfer(ctx, signed, requestBody)
		_ = requestBody.Close()
		if err == nil {
			return response, nil
		}
		if response != nil {
			_ = response.Close()
		}
		var failure *transferFailure
		if !errors.As(err, &failure) {
			failure = newTransferFailure("PUT", signed.URL, err)
		}
		detail := *failure
		detail.attempts = attempt
		if !replayable {
			detail.notRetried = true
		}
		if !detail.retry || !replayable || attempt == 3 || ctx.Err() != nil {
			return nil, errors.Join(&detail, ctx.Err())
		}
		timer := time.NewTimer(time.Duration(attempt) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// safety: net/http can finish closing a failed request asynchronously; fence its reader before rewinding.
type uploadBody struct {
	mu     sync.Mutex
	reader io.Reader
	closed bool
}

func (b *uploadBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	return b.reader.Read(p)
}
func (b *uploadBody) Close() error { b.mu.Lock(); defer b.mu.Unlock(); b.closed = true; return nil }
