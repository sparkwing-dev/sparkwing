package client

import (
	"bytes"
	"context"
	"crypto/md5" // #nosec G501 -- S3's Content-MD5 integrity check, not a security digest
	"encoding/base64"
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

// WatchReport is what a heartbeat reply says about viewers. A node
// report names NodeID and whether a viewer watches it; a run report sets
// WholeRun and lists in Nodes every node of the run a viewer watches.
type WatchReport struct {
	RunID    string
	NodeID   string
	Watched  bool
	WholeRun bool
	Nodes    []string
}

// WithWatchObserver has this client hand fn every watch report a node
// claim or run heartbeat reply carries, so the process that writes a
// node's log knows when to push its lines to the live view. Set it
// before the client serves requests.
func (c *Client) WithWatchObserver(fn func(WatchReport)) *Client {
	c.watchObserver = fn
	return c
}

func (c *Client) askWatched(req *http.Request) {
	if c.watchObserver != nil {
		req.Header.Set(store.LogWatchHeader, "1")
	}
}

func (c *Client) reportWatch(r WatchReport) {
	if c.watchObserver != nil {
		c.watchObserver(r)
	}
}

// KeepsLogSegments reports whether the controller advertises that it
// keeps node logs as segments. A controller that predates segments
// answers false.
func (c *Client) KeepsLogSegments(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, capabilitiesReadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/capabilities", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, readHTTPError(resp)
	}
	var caps struct {
		Logs struct {
			Segments bool `json:"segments"`
		} `json:"logs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil {
		return false, fmt.Errorf("decode capabilities: %w", err)
	}
	return caps.Logs.Segments, nil
}

// LogSegment asks the controller to sign the upload of one log segment
// of a node attempt, or with req.N zero names the attempt the writer
// writes. A controller that keeps no segments answers
// [ErrLogSegmentsUnavailable].
func (c *Client) LogSegment(ctx context.Context, runID, nodeID string, req store.LogSegmentRequest) (store.LogSegmentGrant, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return store.LogSegmentGrant{}, err
	}
	resp, err := c.postLogWrite(ctx, runID, nodeID, "log-segments", body)
	if err != nil {
		return store.LogSegmentGrant{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		if err := classifyNotFound(resp); errors.Is(err, ErrControllerLacksRoute) || errors.Is(err, ErrForeignNotFound) {
			return store.LogSegmentGrant{}, fmt.Errorf("%w: %w", ErrLogSegmentsUnavailable, err)
		}
		return store.LogSegmentGrant{}, fmt.Errorf("log segments: run %s node %s not found", runID, nodeID)
	case resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get("Retry-After") == "":
		return store.LogSegmentGrant{}, fmt.Errorf("%w: %w", ErrLogSegmentsUnavailable, readHTTPError(resp))
	case resp.StatusCode != http.StatusOK:
		return store.LogSegmentGrant{}, fmt.Errorf("log segments: %w", readHTTPError(resp))
	}
	var grant store.LogSegmentGrant
	if err := json.NewDecoder(resp.Body).Decode(&grant); err != nil {
		return store.LogSegmentGrant{}, fmt.Errorf("decode log segment grant: %w", err)
	}
	return grant, nil
}

// SealLog posts the manifest that seals a node attempt's log. The
// controller checks it against the segments it signed and answers
// [logseg.ErrSealed] when the attempt already has a manifest.
func (c *Client) SealLog(ctx context.Context, runID, nodeID string, m logseg.Manifest) error {
	body, err := m.Marshal()
	if err != nil {
		return err
	}
	resp, err := c.postLogWrite(ctx, runID, nodeID, "log-manifest", body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusPreconditionFailed:
		return logseg.ErrSealed
	default:
		return fmt.Errorf("seal log: %w", readHTTPError(resp))
	}
}

func (c *Client) postLogWrite(ctx context.Context, runID, nodeID, route string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+claimNodePath(runID, nodeID, route), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
	setNodeClaimFenceHeaders(req, ctx)
	return c.do(req)
}

const logSegmentPutTimeout = time.Minute

// LogSegmentUploader is the [logseg.Uploader] a node's log sink writes
// through: it has the controller sign each sealed segment for its exact
// size and digests, PUTs it to the signed URL, and posts the manifest to
// the controller.
type LogSegmentUploader struct {
	c             *Client
	base          context.Context
	runID, nodeID string

	mu                  sync.Mutex
	attempt, generation int64
	onWatched           func(bool)
}

var _ logseg.Uploader = (*LogSegmentUploader)(nil)

// LiveAttempt names the attempt the controller granted, as
// [store.LiveLogAttempt] spells it for the live view; empty before Open.
func (u *LogSegmentUploader) LiveAttempt() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.attempt == 0 {
		return ""
	}
	return store.LiveLogAttempt(u.attempt, u.generation)
}

// NewLogSegmentUploader returns an uploader for one node's log. Every
// request it makes carries the values of base, such as the node's claim
// fence; base's cancellation is not inherited.
func (c *Client) NewLogSegmentUploader(base context.Context, runID, nodeID string) *LogSegmentUploader {
	return &LogSegmentUploader{c: c, base: context.WithoutCancel(base), runID: runID, nodeID: nodeID}
}

// OnWatched hands fn the watched flag every reply from the controller
// carries.
func (u *LogSegmentUploader) OnWatched(fn func(bool)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.onWatched = fn
}

// Open asks the controller which attempt the writer writes, which records
// where its log lives. It answers [ErrLogSegmentsUnavailable] when the
// controller keeps no segments, so the caller can keep the log another way.
func (u *LogSegmentUploader) Open(ctx context.Context) error {
	_, err := u.request(ctx, store.LogSegmentRequest{})
	return err
}

func (u *LogSegmentUploader) requestCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	rc, cancel := context.WithTimeout(u.base, logSegmentPutTimeout)
	stop := context.AfterFunc(ctx, cancel)
	return rc, func() {
		stop()
		cancel()
	}
}

func (u *LogSegmentUploader) request(ctx context.Context, req store.LogSegmentRequest) (store.LogSegmentGrant, error) {
	u.mu.Lock()
	req.Attempt, req.Generation = u.attempt, u.generation
	u.mu.Unlock()
	rc, cancel := u.requestCtx(ctx)
	defer cancel()
	grant, err := u.c.LogSegment(rc, u.runID, u.nodeID, req)
	if err != nil {
		return grant, err
	}
	u.mu.Lock()
	u.attempt, u.generation = grant.Attempt, grant.Generation
	onWatched := u.onWatched
	u.mu.Unlock()
	if onWatched != nil {
		onWatched(grant.Watched)
	}
	return grant, nil
}

// PutSegment has the controller sign seg for body's size and digests, then
// uploads body.
func (u *LogSegmentUploader) PutSegment(ctx context.Context, seg logseg.Segment, body []byte) error {
	sum := md5.Sum(body)
	grant, err := u.request(ctx, store.LogSegmentRequest{
		N: seg.N, Size: int64(len(body)), MD5: base64.StdEncoding.EncodeToString(sum[:]), SHA256: seg.SHA256,
	})
	if err != nil {
		return fmt.Errorf("sign log segment %d: %w", seg.N, err)
	}
	if err := u.put(ctx, grant, body); err != nil {
		return fmt.Errorf("upload log segment %d: %w", seg.N, err)
	}
	return nil
}

// PutManifest seals the log, stamped with the attempt the controller
// named.
func (u *LogSegmentUploader) PutManifest(ctx context.Context, m logseg.Manifest) error {
	u.mu.Lock()
	m.Attempt, m.Generation = u.attempt, u.generation
	u.mu.Unlock()
	rc, cancel := u.requestCtx(ctx)
	defer cancel()
	return u.c.SealLog(rc, u.runID, u.nodeID, m)
}

func (u *LogSegmentUploader) put(ctx context.Context, grant store.LogSegmentGrant, body []byte) error {
	rc, cancel := u.requestCtx(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(rc, http.MethodPut, u.c.blobURL(grant.URL), bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range grant.Headers {
		if !strings.EqualFold(k, "Content-Length") && !strings.EqualFold(k, "Host") {
			req.Header.Set(k, v)
		}
	}
	req.ContentLength = int64(len(body))
	resp, err := u.c.blobClient(grant.URL).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d: %w", resp.StatusCode, readHTTPError(resp))
	}
	return nil
}
