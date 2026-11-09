package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// ArtifactReader reads committed files without exposing data-plane credentials.
type ArtifactReader interface {
	ReadRun(context.Context, string) (controllerwire.RunDetail, error)
	ReadArtifacts(context.Context, string, string, int, string) (controllerwire.ArtifactPage, error)
	DownloadArtifact(context.Context, string, string, string, io.Writer) (blob.ManifestEntry, error)
}

// NewArtifactReader uses the selected controller's authenticated transport.
func NewArtifactReader(options EngineOptions) (ArtifactReader, error) {
	return newEngineClient(options)
}

func artifactEndpoint(run, job string) (string, error) {
	if !validSelector(run) || !validSelector(job) {
		return "", &apierr.Error{Code: apierr.BadRequest, Message: "artifact reads require valid run and job selectors"}
	}
	return "/api/v1/runs/" + url.PathEscape(run) + "/jobs/" + url.PathEscape(job) + "/artifacts", nil
}

// ReadArtifacts reads one page of the selected job's committed manifest.
func (c *engineClient) ReadArtifacts(ctx context.Context, run, job string, limit int, cursor string) (controllerwire.ArtifactPage, error) {
	var page controllerwire.ArtifactPage
	endpoint, err := artifactEndpoint(run, job)
	if err != nil {
		return page, err
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	if err := c.request(ctx, "GET", "/api/v1/runs/{run}/jobs/{job}/artifacts", endpoint+"?"+query.Encode(), "", nil, &page); err != nil {
		return controllerwire.ArtifactPage{}, err
	}
	if limit < 1 || len(page.Artifacts) > limit || page.Truncated != (page.NextCursor != "") || page.Truncated && (len(page.Artifacts) == 0 || page.NextCursor == cursor) {
		return controllerwire.ArtifactPage{}, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned inconsistent artifact pagination"}
	}
	return page, nil
}

// DownloadArtifact writes one exact manifest path and verifies its size and digest.
func (c *engineClient) DownloadArtifact(ctx context.Context, run, job, path string, out io.Writer) (blob.ManifestEntry, error) {
	endpoint, err := artifactEndpoint(run, job)
	if err != nil {
		return blob.ManifestEntry{}, err
	}
	var result controllerwire.JobArtifacts
	if err := c.request(ctx, "GET", "/api/v1/runs/{run}/jobs/{job}/artifacts", endpoint+"?"+url.Values{"path": {path}}.Encode(), "", nil, &result); err != nil {
		return blob.ManifestEntry{}, err
	}
	if path == "" || len(result.Artifacts) != 1 || result.Artifacts[0].Path != path || result.Download == nil {
		return blob.ManifestEntry{}, blob.ErrIntegrity
	}
	entry, transfer := result.Artifacts[0], *result.Download
	if entry.Size < 0 || entry.Size != transfer.Size || entry.Digest != transfer.Digest {
		return blob.ManifestEntry{}, blob.ErrIntegrity
	}
	body, err := (blob.TransferClient{Transport: c.http.Transport}).Transfer(ctx, transfer, nil)
	if err != nil {
		return blob.ManifestEntry{}, err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(body, entry.Size))
	var extra [1]byte
	extraN, extraErr := io.ReadFull(body, extra[:])
	if extraN != 0 || extraErr != io.EOF || n != entry.Size || hex.EncodeToString(hash.Sum(nil)) != string(entry.Digest) {
		err = errors.Join(err, blob.ErrIntegrity)
	}
	err = errors.Join(err, body.Close(), ctx.Err())
	return entry, err
}
