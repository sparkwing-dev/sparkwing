package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestArtifactDownloadsVerifyBytesAndKeepControllerCredentialsPrivate(t *testing.T) {
	sum := sha256.Sum256([]byte("file"))
	entry := blob.ManifestEntry{Path: "dir/a b", Digest: blob.Digest(hex.EncodeToString(sum[:])), Size: 4, Mode: 0o644}
	for _, contents := range []string{"file", "evil", "fil", "file-extra"} {
		t.Run(contents, func(t *testing.T) {
			reader, err := NewArtifactReader(EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "private-controller-token", nil }, HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "objects.example" {
					if req.Header.Get("Authorization") != "" {
						t.Fatal("controller credential reached object storage")
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(contents)), Header: make(http.Header)}, nil
				}
				if req.URL.RequestURI() != "/api/v1/runs/run/jobs/job/artifacts?path=dir%2Fa+b" || req.Header.Get("Authorization") != "Bearer private-controller-token" {
					t.Fatalf("artifact request = %s", req.URL)
				}
				body, err := json.Marshal(controllerwire.JobArtifacts{Artifacts: []blob.ManifestEntry{entry}, Download: &blob.SignedTransfer{URL: "https://objects.example/file?signed=value", Digest: entry.Digest, Size: entry.Size}})
				if err != nil {
					t.Fatal(err)
				}
				return engineResponse(http.StatusOK, string(body)), nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			got, err := reader.DownloadArtifact(t.Context(), "run", "job", entry.Path, &out)
			if contents == "file" {
				if err != nil || got != entry || out.String() != contents {
					t.Fatalf("download = %+v, %q, %v", got, out.String(), err)
				}
			} else if err == nil {
				t.Fatalf("accepted corrupt bytes %q", contents)
			}
		})
	}
}

func TestArtifactDownloadFailuresKeepSignedURLsInsideTransfer(t *testing.T) {
	const signedURL = "https://bucket.s3.example/file?X-Amz-Credential=private&X-Amz-Signature=secret"
	for _, stage := range []string{"request", "read", "close"} {
		t.Run(stage, func(t *testing.T) {
			failure := fmt.Errorf("failed to read %s", signedURL)
			entry := blob.ManifestEntry{Path: "file", Size: 4, Digest: blob.Digest(strings.Repeat("a", 64))}
			reader, err := NewArtifactReader(EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "bucket.s3.example" {
					if stage == "request" {
						return nil, failure
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: artifactFailureBody{err: failure, failRead: stage == "read"}}, nil
				}
				body, err := json.Marshal(controllerwire.JobArtifacts{Artifacts: []blob.ManifestEntry{entry}, Download: &blob.SignedTransfer{URL: signedURL, Size: entry.Size, Digest: entry.Digest}})
				if err != nil {
					t.Fatal(err)
				}
				return engineResponse(http.StatusOK, string(body)), nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = reader.DownloadArtifact(t.Context(), "run", "job", "file", io.Discard)
			if err == nil || strings.Contains(fmt.Sprintf("%+v", err), "secret") || strings.Contains(err.Error(), "X-Amz") || errors.Is(err, failure) {
				t.Fatal("download retained signed credentials or failed to report the transfer failure")
			}
		})
	}
}

type artifactFailureBody struct {
	err      error
	failRead bool
}

func (b artifactFailureBody) Read([]byte) (int, error) {
	if b.failRead {
		return 0, b.err
	}
	return 0, io.EOF
}

func (b artifactFailureBody) Close() error { return b.err }

func TestArtifactListingReadsOneBoundedPageWithoutCredentials(t *testing.T) {
	requests := 0
	reader, err := NewArtifactReader(EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.Query().Get("limit") != "2" || req.URL.Query().Get("cursor") != "previous" || req.URL.Query().Has("path") {
			t.Fatalf("wrong page request: %s", req.URL.RequestURI())
		}
		return engineResponse(http.StatusOK, `{"artifacts":[{"path":"file","digest":"abc","size":0,"mode":0}],"truncated":true,"next_cursor":"next"}`), nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.ReadArtifacts(t.Context(), "run", "job", 2, "previous")
	if err != nil || requests != 1 || len(page.Artifacts) != 1 || !page.Truncated || page.NextCursor != "next" {
		t.Fatalf("page = %+v, %v, requests=%d", page, err, requests)
	}
}
