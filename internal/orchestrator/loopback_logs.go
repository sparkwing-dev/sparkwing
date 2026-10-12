package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a refusal is permanent, such as a request without the node's credential, so the writer stops
// rather than retrying into it.
var errLogRefused = errors.New("node log endpoint refused the request")

type appendRangeKey struct{}

type appendRange struct {
	stream      string
	first, last int64
}

func withAppendRange(ctx context.Context, stream string, first, last int64) context.Context {
	return context.WithValue(ctx, appendRangeKey{}, appendRange{stream: stream, first: first, last: last})
}

func appendRangeFrom(ctx context.Context) (appendRange, bool) {
	r, ok := ctx.Value(appendRangeKey{}).(appendRange)
	return r, ok
}

// safety: a pipeline process posts its node's log to the engine process that supervises it, which keeps it as
// segments; it never writes to a store of its own, so its reads are refused.
type loopbackLogs struct {
	baseURL string
	http    *http.Client
	token   string
}

var (
	_ storage.LogStore = (*loopbackLogs)(nil)
	_ logSealer        = (*loopbackLogs)(nil)
)

func newLoopbackLogs(baseURL string, httpClient *http.Client, token string) *loopbackLogs {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &loopbackLogs{baseURL: baseURL, http: httpClient, token: token}
}

func (c *loopbackLogs) nodeURL(runID, nodeID string) string {
	return c.baseURL + "/api/v1/logs/" + url.PathEscape(runID) + "/" + url.PathEscape(nodeID)
}

// safety: the claim headers ride every request, because one endpoint can serve several nodes' claims and
// repeats each one's fence on the uploads it makes for that node.
func (c *loopbackLogs) post(ctx context.Context, target, contentType string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if fence, ok := store.NodeClaimFenceFromContext(ctx); ok {
		req.Header.Set(store.ClaimHolderHeader, fence.HolderID)
		req.Header.Set(store.ClaimMembershipHeader, fence.MembershipID)
		req.Header.Set(store.ClaimReservationHeader, fence.ReservationID)
		req.Header.Set(store.ClaimGenerationHeader, strconv.FormatInt(fence.ClaimGeneration, 10))
	}
	if fence, ok := store.TriggerClaimFenceFromContext(ctx); ok {
		req.Header.Set(store.TriggerGenerationHeader, strconv.FormatInt(fence.ClaimGeneration, 10))
	}
	if r, ok := appendRangeFrom(ctx); ok {
		req.Header.Set(logseg.LogStreamHeader, r.stream)
		req.Header.Set(logseg.LogSeqHeader, strconv.FormatInt(r.first, 10))
		if r.last != r.first {
			req.Header.Set(logseg.LogSeqEndHeader, strconv.FormatInt(r.last, 10))
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	msg, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	err = errors.Join(fmt.Errorf("node log endpoint answered HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(msg)), rerr)
	if resp.StatusCode/100 == 4 {
		return fmt.Errorf("%w: %w", errLogRefused, err)
	}
	return err
}

func (c *loopbackLogs) Append(ctx context.Context, runID, nodeID string, data []byte) error {
	return c.post(ctx, c.nodeURL(runID, nodeID), "text/plain; charset=utf-8", data)
}

func (c *loopbackLogs) Seal(ctx context.Context, runID, nodeID string, seal logseg.WriterSeal) error {
	body, err := json.Marshal(seal)
	if err != nil {
		return err
	}
	return c.post(ctx, c.nodeURL(runID, nodeID)+"/seal", "application/json", body)
}

func (*loopbackLogs) Read(context.Context, string, string, storage.ReadOpts) ([]byte, error) {
	return nil, fmt.Errorf("%w: read node logs through the controller", storage.ErrNotSupported)
}

func (*loopbackLogs) ReadRun(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("%w: read node logs through the controller", storage.ErrNotSupported)
}

func (*loopbackLogs) Stream(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}

func (*loopbackLogs) DeleteRun(context.Context, string) error {
	return fmt.Errorf("%w: delete the run to delete its logs", storage.ErrNotSupported)
}
