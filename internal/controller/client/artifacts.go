package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// ArtifactReader reads committed files without exposing data-plane credentials.
type ArtifactReader interface {
	ReadRun(context.Context, string) (controllerwire.RunDetail, error)
	ReadArtifacts(context.Context, string, string) ([]blob.ManifestEntry, error)
	DownloadArtifact(context.Context, string, string, string, io.Writer) (blob.ManifestEntry, error)
}

// NewArtifactReader uses the selected controller's authenticated transport.
func NewArtifactReader(options EngineOptions) (ArtifactReader, error) {
	return newEngineClient(options)
}

func (c *engineClient) jobArtifacts(ctx context.Context, run, job, path string) (controllerwire.JobArtifacts, error) {
	var result controllerwire.JobArtifacts
	if !validSelector(run) || !validSelector(job) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "artifact reads require valid run and job selectors"}
	}
	endpoint := "/api/v1/runs/" + url.PathEscape(run) + "/jobs/" + url.PathEscape(job) + "/artifacts"
	if path != "" {
		endpoint += "?" + url.Values{"path": {path}}.Encode()
	}
	err := c.request(ctx, "GET", "/api/v1/runs/{run}/jobs/{job}/artifacts", endpoint, "", nil, &result)
	return result, err
}

// ReadArtifacts lists the selected job’s committed manifest.
func (c *engineClient) ReadArtifacts(ctx context.Context, run, job string) ([]blob.ManifestEntry, error) {
	result, err := c.jobArtifacts(ctx, run, job, "")
	return result.Artifacts, err
}

// DownloadArtifact writes one exact manifest path and verifies its size and digest.
func (c *engineClient) DownloadArtifact(ctx context.Context, run, job, path string, out io.Writer) (blob.ManifestEntry, error) {
	result, err := c.jobArtifacts(ctx, run, job, path)
	if err != nil {
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
