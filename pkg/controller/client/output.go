package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// perf: an output is at most 64 MiB, which a slow link can take minutes to move.
const outputTransferTimeout = 10 * time.Minute

// UploadNodeOutput stores a node's output bytes as a committed object and
// returns the ref its finish or attempt report names.
func (c *Client) UploadNodeOutput(ctx context.Context, runID, nodeID string, data []byte) (*store.OutputRef, error) {
	if len(data) == 0 {
		return nil, nil
	}
	sum := sha256.Sum256(data)
	ref := &store.OutputRef{Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	var grant store.OutputUploadGrant
	if err := c.post(ctx, claimNodePath(runID, nodeID, "output-upload"),
		map[string]any{"size": ref.Size, "sha256": ref.SHA256}, http.StatusOK, &grant); err != nil {
		return nil, fmt.Errorf("reserve output upload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.blobURL(grant.URL), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	for k, v := range grant.Headers {
		if !strings.EqualFold(k, "Content-Length") && !strings.EqualFold(k, "Host") {
			req.Header.Set(k, v)
		}
	}
	req.ContentLength = ref.Size
	req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
	resp, err := c.blobClient(grant.URL).Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload output: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("upload output: %w", readHTTPError(resp))
	}
	if err := c.post(ctx, claimNodePath(runID, nodeID, "output-commit"),
		map[string]string{"upload_id": grant.UploadID}, http.StatusNoContent, nil); err != nil {
		return nil, fmt.Errorf("commit output upload: %w", err)
	}
	ref.Key = grant.Key
	return ref, nil
}

// GetNodeOutput returns the raw JSON output of a finished node, or
// ErrNotFound if the node doesn't exist. 409 if it exists but hasn't
// finished. A node that recorded no output reads as JSON null.
func (c *Client) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	return c.readOutput(ctx, c.baseURL+claimNodePath(runID, nodeID, "output"))
}

// GetChildNodeOutput reads a node's output from childID, a child run runID
// started, through the calling claim.
func (c *Client) GetChildNodeOutput(ctx context.Context, runID, childID, nodeID string) ([]byte, error) {
	return c.readOutput(ctx, c.baseURL+childPath(runID, childID)+"/nodes/"+url.PathEscape(nodeID)+"/output")
}

func (c *Client) readOutput(ctx context.Context, u string) ([]byte, error) {
	grant, err := c.outputGrant(ctx, u)
	if err != nil {
		return nil, err
	}
	return c.fetchOutput(ctx, grant)
}

func (c *Client) outputGrant(ctx context.Context, u string) (store.OutputReadGrant, error) {
	var grant store.OutputReadGrant
	err := c.getJSON(ctx, u, &grant)
	return grant, err
}

// safety: the bytes are checked against the digest the controller recorded
// at commit, so a URL that serves anything else is refused.
func (c *Client) fetchOutput(ctx context.Context, grant store.OutputReadGrant) ([]byte, error) {
	if grant.URL == "" {
		return []byte("null"), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.blobURL(grant.URL), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
	resp, err := c.blobClient(grant.URL).Do(req)
	if err != nil {
		return nil, fmt.Errorf("download output: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, store.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download output: %w", readHTTPError(resp))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, store.MaxOutputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download output: %w", err)
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != grant.Size || hex.EncodeToString(sum[:]) != grant.SHA256 {
		return nil, fmt.Errorf("download output: %w", store.ErrOutputCorrupt)
	}
	return data, nil
}

func (c *Client) blobURL(u string) string {
	if strings.HasPrefix(u, "/") {
		return c.baseURL + u
	}
	return u
}

// safety: a signed URL carries its own authority, so the bearer never
// travels with it, and a URL off this controller never sees its transport.
func (c *Client) blobClient(u string) *http.Client {
	base, baseErr := url.Parse(c.baseURL)
	target, targetErr := url.Parse(c.blobURL(u))
	if baseErr != nil || targetErr != nil || !strings.EqualFold(base.Scheme, target.Scheme) || !strings.EqualFold(base.Host, target.Host) {
		return &http.Client{Timeout: outputTransferTimeout, Transport: otelutil.WrapTransport(nil)}
	}
	transport := c.http.Transport
	return &http.Client{Timeout: outputTransferTimeout, Transport: transport}
}
