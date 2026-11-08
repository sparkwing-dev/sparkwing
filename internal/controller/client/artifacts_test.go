package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(contents)), Header: make(http.Header)}, nil
				}
				if req.URL.RequestURI() != "/api/v1/runs/run/jobs/job/artifacts?path=dir%2Fa+b" || req.Header.Get("Authorization") != "Bearer private-controller-token" {
					t.Fatalf("artifact request = %s", req.URL)
				}
				body, err := json.Marshal(controllerwire.JobArtifacts{Artifacts: []blob.ManifestEntry{entry}, Download: &blob.SignedTransfer{URL: "https://objects.example/file?signed=value", Digest: entry.Digest, Size: entry.Size}})
				if err != nil {
					t.Fatal(err)
				}
				return engineResponse(200, string(body)), nil
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
