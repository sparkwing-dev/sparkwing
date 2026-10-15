package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// perf: large outputs can take minutes to transfer over a slow link.
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
	ctx, cancel := context.WithTimeout(ctx, outputTransferTimeout)
	defer cancel()
	body, err := (blob.TransferClient{Transport: c.blobTransport(grant.URL)}).Transfer(ctx, blob.SignedTransfer{
		URL: c.blobURL(grant.URL), Headers: grant.Headers, Size: ref.Size, Digest: blob.Digest(ref.SHA256),
	}, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("upload output: %w", err)
	}
	if err := body.Close(); err != nil {
		return nil, err
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
	if grant.Size < 0 || grant.Size > limits.NodeOutputBytes {
		return nil, store.ErrOutputCorrupt
	}
	ctx, cancel := context.WithTimeout(ctx, outputTransferTimeout)
	defer cancel()
	body, err := (blob.TransferClient{Transport: c.blobTransport(grant.URL)}).Transfer(ctx, blob.SignedTransfer{
		URL: c.blobURL(grant.URL), Size: grant.Size, Digest: blob.Digest(grant.SHA256),
	}, nil)
	if errors.Is(err, blob.ErrNotFound) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("download output: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(body, limits.NodeOutputBytes+1))
	if err := errors.Join(err, body.Close()); err != nil {
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
func (c *Client) blobTransport(u string) http.RoundTripper {
	base, baseErr := url.Parse(c.baseURL)
	target, targetErr := url.Parse(c.blobURL(u))
	if baseErr != nil || targetErr != nil || !strings.EqualFold(base.Scheme, target.Scheme) || !strings.EqualFold(base.Host, target.Host) {
		return otelutil.WrapTransport(nil)
	}
	return c.http.Transport
}
