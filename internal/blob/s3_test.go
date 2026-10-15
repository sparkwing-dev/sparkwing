package blob_test

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type s3HTTP func(*http.Request) (*http.Response, error)

func (f s3HTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestS3SigningAndCommit(t *testing.T) {
	object := blob.Blob{Team: "team", Digest: digest("hello"), Size: 5}
	raw, _ := hex.DecodeString(string(object.Digest))
	checksum := base64.StdEncoding.EncodeToString(raw)
	pending := "teams/team/pending/ticket"
	final, err := blob.ObjectKey(object.Team, object.Digest)
	if err != nil {
		t.Fatal(err)
	}
	objects := map[string]string{pending: checksum}
	client := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String("https://objects.invalid"), UsePathStyle: true, RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test-secret"}, nil
		}),
		HTTPClient: s3HTTP(func(r *http.Request) (*http.Response, error) {
			response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}
			key := strings.TrimPrefix(r.URL.Path, "/bucket/")
			switch r.Method {
			case http.MethodGet:
				if sum, ok := objects[key]; ok {
					response.Header.Set("Content-Length", "5")
					response.ContentLength = 5
					response.Header.Set("x-amz-checksum-sha256", sum)
					response.Header.Set("ETag", `"staged-version"`)
					body := "hello"
					if sum != checksum {
						body = "jello"
					}
					response.Body = io.NopCloser(strings.NewReader(body))
				} else {
					response.StatusCode = http.StatusNotFound
				}
			case http.MethodPut:
				response.Body = io.NopCloser(strings.NewReader(`<CopyObjectResult><ETag>"final-version"</ETag></CopyObjectResult>`))
				if r.Header.Get("If-None-Match") != "*" || r.Header.Get("X-Amz-Copy-Source-If-Match") != `"staged-version"` {
					t.Fatalf("unfenced copy: %v", r.Header)
				}
				source, _ := url.PathUnescape(r.Header.Get("X-Amz-Copy-Source"))
				if source != "bucket/"+pending || key != final {
					t.Fatalf("copy %q -> %q", source, key)
				}
				if _, exists := objects[key]; exists {
					response.StatusCode = http.StatusPreconditionFailed
					response.Body = io.NopCloser(strings.NewReader(`<Error><Code>PreconditionFailed</Code></Error>`))
				} else {
					objects[key] = objects[pending]
				}
			case http.MethodDelete:
				delete(objects, key)
			default:
				t.Fatalf("unexpected request: %s", r.Method)
			}
			return response, nil
		}),
	})
	backend, err := blob.NewS3(client, "bucket")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := backend.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodPut, Key: pending, Size: 5, SHA256: string(object.Digest), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(signed.URL)
	if err != nil {
		t.Fatal(err)
	}
	bound := u.Query().Get("X-Amz-SignedHeaders")
	if !strings.Contains(bound, "content-length") || !strings.Contains(bound, "x-amz-checksum-sha256") {
		t.Fatalf("unbound upload: %s", bound)
	}
	if signed.Headers["X-Amz-Checksum-Sha256"] != checksum {
		t.Fatalf("checksum headers: %v", signed.Headers)
	}
	if err := backend.CommitObject(t.Context(), pending, object); err != nil {
		t.Fatal(err)
	}
	if err := backend.CommitObject(t.Context(), pending, object); err != nil {
		t.Fatalf("deduplicated commit: %v", err)
	}
	if err := backend.DeleteObject(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	if err := backend.CommitObject(t.Context(), pending, object); err != nil {
		t.Fatalf("interrupted commit: %v", err)
	}
	objects[final] = "different checksum"
	if err := backend.CommitObject(t.Context(), pending, object); !errors.Is(err, blob.ErrIntegrity) {
		t.Fatalf("corrupt final: %v", err)
	}
	get, err := backend.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodGet, Key: final, Size: 5, SHA256: string(object.Digest), ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(get.URL)
	seconds, err := strconv.Atoi(u.Query().Get("X-Amz-Expires"))
	if err != nil || seconds < 1 || seconds > 60 {
		t.Fatalf("download expiry: %s", u.RawQuery)
	}
}
