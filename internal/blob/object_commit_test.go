package blob_test

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestS3CommitVerifiesStoredBytes(t *testing.T) {
	closeErr := errors.New("object close failed")
	for _, tc := range []struct {
		name, staged, final string
		closeKey            string
		fixedLength         bool
		want                error
		copies              int
	}{
		{name: "new", staged: "hello", copies: 1},
		{name: "corrupt staging", staged: "jello", want: blob.ErrIntegrity},
		{name: "short staging", staged: "hell", want: blob.ErrIntegrity},
		{name: "long staging", staged: "hello!", want: blob.ErrIntegrity},
		{name: "truncated body", staged: "hell", fixedLength: true, want: blob.ErrIntegrity},
		{name: "extra body", staged: "hello!", fixedLength: true, want: blob.ErrIntegrity},
		{name: "deduplicated", staged: "hello", final: "hello", copies: 1},
		{name: "corrupt final", staged: "hello", final: "jello", want: blob.ErrIntegrity, copies: 1},
		{name: "missing staging with existing final", final: "hello", want: blob.ErrNotFound},
		{name: "missing staging with corrupt final", final: "jello", want: blob.ErrNotFound},
		{name: "absent", want: blob.ErrNotFound},
		{name: "staging close", staged: "hello", closeKey: "pending", want: closeErr},
		{name: "final close", staged: "hello", closeKey: "cas", want: closeErr, copies: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := blob.Blob{Team: "team", Digest: digest("hello"), Size: 5}
			final, err := blob.ObjectKey(object.Team, object.Digest)
			if err != nil {
				t.Fatal(err)
			}
			pending := "teams/team/pending/ticket"
			objects := map[string]string{}
			if tc.staged != "" {
				objects[pending] = tc.staged
			}
			if tc.final != "" {
				objects[final] = tc.final
			}
			copies, reads, closes := 0, 0, 0
			raw, _ := hex.DecodeString(string(object.Digest))
			client := s3.New(s3.Options{
				Region: "us-east-1", BaseEndpoint: aws.String("https://objects.invalid"), UsePathStyle: true, RetryMaxAttempts: 1,
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "secret"}, nil
				}),
				HTTPClient: s3HTTP(func(r *http.Request) (*http.Response, error) {
					key := strings.TrimPrefix(r.URL.Path, "/bucket/")
					response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(""))}
					switch r.Method {
					case http.MethodHead, http.MethodGet:
						body, ok := objects[key]
						if !ok {
							response.StatusCode = http.StatusNotFound
							break
						}
						response.ContentLength = int64(len(body))
						if tc.fixedLength {
							response.ContentLength = object.Size
						}
						response.Header.Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
						response.Header.Set("ETag", `"immutable-version"`)
						// safety: metadata alone cannot prove the bytes returned by storage.
						response.Header.Set("x-amz-checksum-sha256", base64.StdEncoding.EncodeToString(raw))
						if r.Method == http.MethodGet {
							reads++
							var failure error
							if strings.Contains(key, "/"+tc.closeKey+"/") && tc.closeKey != "" {
								failure = tc.want
							}
							response.Body = closeCounter{Reader: strings.NewReader(body), closeErr: failure, count: &closes}
						}
					case http.MethodPut:
						copies++
						if r.Header.Get("If-None-Match") != "*" || r.Header.Get("X-Amz-Copy-Source-If-Match") != `"immutable-version"` {
							t.Fatal("copy did not preserve the verified version")
						}
						if _, exists := objects[key]; exists {
							response.StatusCode = http.StatusPreconditionFailed
							response.Body = io.NopCloser(strings.NewReader(`<Error><Code>PreconditionFailed</Code></Error>`))
						} else {
							objects[key] = objects[pending]
							response.Body = io.NopCloser(strings.NewReader(`<CopyObjectResult><ETag>"final"</ETag></CopyObjectResult>`))
						}
					default:
						t.Fatalf("unexpected operation %s", r.Method)
					}
					return response, nil
				}),
			})
			backend, err := blob.NewS3(client, "bucket", "")
			if err != nil {
				t.Fatal(err)
			}
			err = backend.CommitObject(t.Context(), pending, object)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("commit = %v, want %v", err, tc.want)
			}
			if copies != tc.copies || reads == 0 && tc.want == nil || closes != reads {
				t.Fatalf("copies/reads/closes = %d/%d/%d, want %d copies and closed byte reads", copies, reads, closes, tc.copies)
			}
		})
	}
}

type closeCounter struct {
	io.Reader
	closeErr error
	count    *int
}

func (b closeCounter) Close() error { *b.count++; return b.closeErr }

func TestS3CommitRequiresItsOwnStagedBytes(t *testing.T) {
	random := rand.New(rand.NewPCG(29, 66))
	for i := range 32 {
		body := make([]byte, i*17)
		for j := range body {
			body[j] = byte(random.Uint32())
		}
		object := blob.Blob{Team: "team", Digest: digest(string(body)), Size: int64(len(body))}
		pending := "teams/team/pending/unuploaded"
		final, err := blob.ObjectKey(object.Team, object.Digest)
		if err != nil {
			t.Fatal(err)
		}
		client := s3.New(s3.Options{
			Region: "us-east-1", BaseEndpoint: aws.String("https://objects.invalid"), UsePathStyle: true, RetryMaxAttempts: 1,
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "secret"}, nil
			}),
			HTTPClient: s3HTTP(func(r *http.Request) (*http.Response, error) {
				key := strings.TrimPrefix(r.URL.Path, "/bucket/")
				response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(""))}
				switch {
				case r.Method == http.MethodGet && key == final:
					response.ContentLength = int64(len(body))
					response.Header.Set("Content-Length", strconv.Itoa(len(body)))
					response.Body = io.NopCloser(strings.NewReader(string(body)))
				case r.Method == http.MethodGet && key == pending:
					response.StatusCode = http.StatusNotFound
				default:
					t.Fatalf("unexpected object access: %s %s", r.Method, key)
				}
				return response, nil
			}),
		})
		backend, err := blob.NewS3(client, "bucket", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.CommitObject(t.Context(), pending, object); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("case %d accepted final bytes without this ticket's staged upload: %v", i, err)
		}
	}
}
