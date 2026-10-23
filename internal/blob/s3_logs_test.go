package blob

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

type s3LogBody struct {
	io.Reader
	closed bool
}

func (b *s3LogBody) Close() error { b.closed = true; return nil }

func logS3(t *testing.T, request s3HTTP) *S3 {
	t.Helper()
	return logS3Prefix(t, "", request)
}

func logS3Prefix(t *testing.T, prefix string, request s3HTTP) *S3 {
	t.Helper()
	client := logS3Client(request)
	backend, err := NewS3(client, "bucket", prefix)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func logS3Client(request s3HTTP) *s3.Client {
	return s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String("https://objects.invalid"), UsePathStyle: true, RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test-secret"}, nil
		}),
		HTTPClient: request,
	})
}

func TestS3LogReadsExactBoundedObjects(t *testing.T) {
	for _, name := range []string{"seg-000001.ndjson.gz", "manifest.json"} {
		t.Run(name, func(t *testing.T) {
			key := "logs/r90/teams/acme/runs/run-1/units/plan/g1/" + name
			limit := limits.LogSegmentUploadBytes
			if name == "manifest.json" {
				limit = limits.LogManifestBytes
			}
			for _, size := range []int64{5, -1, limit + 1} {
				body := &s3LogBody{Reader: strings.NewReader("hello")}
				backend := logS3(t, func(r *http.Request) (*http.Response, error) {
					if r.Method != http.MethodGet || r.URL.Path != "/bucket/"+key || r.URL.Query().Has("prefix") || r.URL.Query().Has("list-type") {
						t.Fatalf("unexpected object request: %s %s", r.Method, r.URL.Path)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Length": {strconv.FormatInt(size, 10)}}, ContentLength: size, Body: body, Request: r}, nil
				})
				got, err := backend.Get(t.Context(), key)
				if size != 5 {
					if got != nil || !errors.Is(err, ErrIntegrity) || !body.closed {
						t.Fatalf("invalid length %d = %v, %v; closed = %v", size, got, err, body.closed)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(got)
				if err = errors.Join(err, got.Close()); err != nil || string(raw) != "hello" || !body.closed {
					t.Fatalf("read = %q, %v; closed = %v", raw, err, body.closed)
				}
			}
		})
	}
}

func TestS3LogReadRefusals(t *testing.T) {
	key := "logs/r30/teams/acme/runs/run-1/units/trigger/g1/seg-000001.ndjson.gz"
	backend := logS3(t, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`<Error><Code>NoSuchKey</Code></Error>`)), Request: r}, nil
	})
	if body, err := backend.Get(t.Context(), key); body != nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing log = %v, %v", body, err)
	}
	backend = logS3(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid read reached S3")
		return nil, errors.New("unexpected request")
	})
	for _, key := range []string{"teams/acme/cas/" + string(digest("x")), "../escape", "logs/r+1/teams/acme/runs/r/units/plan/g1/manifest.json", "logs/r90/teams/acme/runs/r/units/plan/g1/other"} {
		if body, err := backend.Get(t.Context(), key); body != nil || err == nil {
			t.Errorf("invalid log key %q = %v, %v", key, body, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if body, err := backend.Get(ctx, key); body != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read = %v, %v", body, err)
	}
}
