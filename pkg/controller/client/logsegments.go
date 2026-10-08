package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ErrLogSegmentsUnavailable reports a controller that keeps no log
// segments: it predates the route, or it has no store to keep them in.
var ErrLogSegmentsUnavailable = errors.New("controller/client: the controller keeps no log segments")

// LogSegments asks the controller for upload URLs for a node attempt's
// log segments. A controller that keeps no segments answers
// [ErrLogSegmentsUnavailable].
func (c *Client) LogSegments(ctx context.Context, runID, nodeID string, req store.LogSegmentsRequest) (store.LogSegmentsGrant, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return store.LogSegmentsGrant{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+claimNodePath(runID, nodeID, "log-segments"), bytes.NewReader(body))
	if err != nil {
		return store.LogSegmentsGrant{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
	setNodeClaimFenceHeaders(httpReq, ctx)
	resp, err := c.do(httpReq)
	if err != nil {
		return store.LogSegmentsGrant{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		if err := classifyNotFound(resp); errors.Is(err, ErrControllerLacksRoute) || errors.Is(err, ErrForeignNotFound) {
			return store.LogSegmentsGrant{}, fmt.Errorf("%w: %w", ErrLogSegmentsUnavailable, err)
		}
		return store.LogSegmentsGrant{}, fmt.Errorf("log segments: run %s node %s not found", runID, nodeID)
	case resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get("Retry-After") == "":
		return store.LogSegmentsGrant{}, fmt.Errorf("%w: %w", ErrLogSegmentsUnavailable, readHTTPError(resp))
	case resp.StatusCode != http.StatusOK:
		return store.LogSegmentsGrant{}, fmt.Errorf("log segments: %w", readHTTPError(resp))
	}
	var grant store.LogSegmentsGrant
	if err := json.NewDecoder(resp.Body).Decode(&grant); err != nil {
		return store.LogSegmentsGrant{}, fmt.Errorf("decode log segments grant: %w", err)
	}
	return grant, nil
}

const (
	logSegmentLowWater     = 4
	logSegmentRefreshAhead = 2 * time.Minute
	logSegmentPutTimeout   = time.Minute
)

// LogSegmentUploader is the [logseg.Uploader] a node's log sink writes
// through: it asks this client's controller for presigned URLs in
// batches and PUTs each segment and the manifest to them.
type LogSegmentUploader struct {
	c             *Client
	base          context.Context
	runID, nodeID string

	mu       sync.Mutex
	grant    store.LogSegmentsGrant
	urls     map[int]store.LogSegmentURL
	through  int
	stored   int64
	received bool
}

var _ logseg.Uploader = (*LogSegmentUploader)(nil)

// NewLogSegmentUploader returns an uploader for one node's log. Every
// request it makes carries the values of base, such as the node's claim
// fence; base's cancellation is not inherited.
func (c *Client) NewLogSegmentUploader(base context.Context, runID, nodeID string) *LogSegmentUploader {
	return &LogSegmentUploader{c: c, base: context.WithoutCancel(base), runID: runID, nodeID: nodeID, urls: map[int]store.LogSegmentURL{}}
}

// Open takes the attempt's first URL batch, which records where its log
// lives. It answers [ErrLogSegmentsUnavailable] when the controller
// keeps no segments, so the caller can keep the log another way.
func (u *LogSegmentUploader) Open(ctx context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.refreshLocked(ctx, 1)
}

func (u *LogSegmentUploader) requestCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	rc, cancel := context.WithTimeout(u.base, logSegmentPutTimeout)
	stop := context.AfterFunc(ctx, cancel)
	return rc, func() {
		stop()
		cancel()
	}
}

// safety: the caller holds mu.
func (u *LogSegmentUploader) refreshLocked(ctx context.Context, next int) error {
	rc, cancel := u.requestCtx(ctx)
	defer cancel()
	req := store.LogSegmentsRequest{Next: next, Count: 16, UploadedThrough: u.through, StoredBytes: u.stored}
	if u.received {
		req.Attempt, req.Generation = u.grant.Attempt, u.grant.Generation
	}
	grant, err := u.c.LogSegments(rc, u.runID, u.nodeID, req)
	if err != nil {
		return err
	}
	u.grant, u.received = grant, true
	clear(u.urls)
	for _, s := range grant.URLs {
		u.urls[s.N] = s
	}
	return nil
}

// safety: the caller holds mu. A refresh the controller refuses, as it
// does once the node's claim has ended, still leaves the URLs of the
// batch in hand usable until it expires; the last segments of a node
// that already reported its outcome go up through them.
func (u *LogSegmentUploader) urlLocked(ctx context.Context, n int) (store.LogSegmentURL, error) {
	s, held := u.urls[n]
	live := u.received && time.Now().Before(u.grant.ExpiresAt)
	fresh := u.received && time.Until(u.grant.ExpiresAt) > logSegmentRefreshAhead
	_, ahead := u.urls[n+logSegmentLowWater-1]
	if held && fresh && ahead {
		return s, nil
	}
	err := u.refreshLocked(ctx, n)
	if err != nil {
		if held && live {
			return s, nil
		}
		return store.LogSegmentURL{}, err
	}
	s, ok := u.urls[n]
	if !ok {
		return store.LogSegmentURL{}, fmt.Errorf("log segments: the controller issued no URL for segment %d", n)
	}
	return s, nil
}

// KeepFresh renews the URL batch before it expires until ctx ends, so a
// node silent for longer than a batch lives still holds a usable
// manifest URL when it finishes. onError hears each renewal that
// failed; the next tick tries again.
func (u *LogSegmentUploader) KeepFresh(ctx context.Context, onError func(error)) {
	t := time.NewTicker(logSegmentRefreshAhead / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		u.mu.Lock()
		if u.received && time.Until(u.grant.ExpiresAt) <= logSegmentRefreshAhead {
			if err := u.refreshLocked(ctx, u.through+1); err != nil && ctx.Err() == nil {
				onError(err)
			}
		}
		u.mu.Unlock()
	}
}

// PutSegment uploads one segment.
func (u *LogSegmentUploader) PutSegment(ctx context.Context, seg logseg.Segment, body []byte) error {
	u.mu.Lock()
	target, err := u.urlLocked(ctx, seg.N)
	u.mu.Unlock()
	if err != nil {
		return err
	}
	if err := u.put(ctx, target, body); err != nil {
		return fmt.Errorf("upload log segment %d: %w", seg.N, err)
	}
	u.mu.Lock()
	u.through = max(u.through, seg.N)
	u.stored += seg.StoredBytes
	u.mu.Unlock()
	return nil
}

// PutManifest writes the manifest that seals the log, stamped with the
// attempt the controller named.
func (u *LogSegmentUploader) PutManifest(ctx context.Context, m logseg.Manifest) error {
	u.mu.Lock()
	if !u.received || time.Until(u.grant.ExpiresAt) <= logSegmentRefreshAhead {
		err := u.refreshLocked(ctx, u.through+1)
		if err != nil && (!u.received || !time.Now().Before(u.grant.ExpiresAt)) {
			u.mu.Unlock()
			return err
		}
	}
	target := u.grant.ManifestURL
	m.Attempt, m.Generation = u.grant.Attempt, u.grant.Generation
	u.mu.Unlock()
	raw, err := m.Marshal()
	if err != nil {
		return err
	}
	err = u.put(ctx, target, raw)
	var status *putStatusError
	if errors.As(err, &status) && status.code == http.StatusPreconditionFailed {
		return logseg.ErrSealed
	}
	if err != nil {
		return fmt.Errorf("upload log manifest: %w", err)
	}
	return nil
}

type putStatusError struct {
	code int
	err  error
}

func (e *putStatusError) Error() string { return fmt.Sprintf("HTTP %d: %v", e.code, e.err) }
func (e *putStatusError) Unwrap() error { return e.err }

func (u *LogSegmentUploader) put(ctx context.Context, target store.LogSegmentURL, body []byte) error {
	rc, cancel := u.requestCtx(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(rc, http.MethodPut, u.c.blobURL(target.URL), bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range target.Headers {
		if !strings.EqualFold(k, "Content-Length") && !strings.EqualFold(k, "Host") {
			req.Header.Set(k, v)
		}
	}
	req.ContentLength = int64(len(body))
	resp, err := u.c.blobClient(target.URL).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return &putStatusError{code: resp.StatusCode, err: readHTTPError(resp)}
	}
	return nil
}
