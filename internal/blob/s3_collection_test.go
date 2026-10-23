package blob

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3CollectionRequiresObservedETag(t *testing.T) {
	for _, state := range []string{"unchanged", "replaced", "absent", "no_etag"} {
		t.Run(state, func(t *testing.T) {
			heads, deletes := 0, 0
			present := state != "absent"
			client := s3.New(s3.Options{
				Region: "us-east-1", BaseEndpoint: aws.String("https://objects.invalid"), UsePathStyle: true, RetryMaxAttempts: 1,
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "secret"}, nil
				}),
				HTTPClient: s3HTTP(func(r *http.Request) (*http.Response, error) {
					response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(""))}
					switch r.Method {
					case http.MethodHead:
						heads++
						if !present {
							response.StatusCode = http.StatusNotFound
						} else if state != "no_etag" {
							response.Header.Set("ETag", `"observed"`)
						}
					case http.MethodDelete:
						deletes++
						if r.Header.Get("If-Match") != `"observed"` {
							t.Fatalf("unconditional collection: %v", r.Header)
						}
						if state == "replaced" {
							response.StatusCode = http.StatusPreconditionFailed
							response.Body = io.NopCloser(strings.NewReader(`<Error><Code>PreconditionFailed</Code></Error>`))
						} else {
							present = false
						}
					default:
						t.Fatalf("unexpected operation: %s", r.Method)
					}
					return response, nil
				}),
			})
			backend, err := NewS3(client, "bucket", "")
			if err != nil {
				t.Fatal(err)
			}
			err = backend.DeleteBlob(t.Context(), Blob{Team: "team", Digest: digest("bytes"), Size: 5})
			wantErr := state == "replaced" || state == "no_etag"
			wantDeletes := 1
			if state == "absent" || state == "no_etag" {
				wantDeletes = 0
			}
			if heads != 1 || deletes != wantDeletes || (err != nil) != wantErr || present != wantErr {
				t.Fatalf("heads=%d deletes=%d present=%t error=%v", heads, deletes, present, err)
			}
		})
	}
}

func TestS3MissingReferencedBytesFail(t *testing.T) {
	client := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String("https://objects.invalid"), UsePathStyle: true, RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "secret"}, nil
		}),
		HTTPClient: s3HTTP(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(`<Error><Code>NoSuchKey</Code></Error>`))}, nil
		}),
	})
	backend, err := NewS3(client, "bucket", "")
	if err != nil {
		t.Fatal(err)
	}
	object := Blob{Team: "team", Digest: digest("null"), Size: 4}
	if err := backend.VerifyBlob(t.Context(), object); !errors.Is(err, ErrNotFound) {
		t.Fatalf("verification: %v", err)
	}
	if body, err := backend.ReadOutput(t.Context(), object); body != nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("output: %q, %v", body, err)
	}
	if members, err := backend.ReadManifest(t.Context(), object.Team, object.Digest); members != nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("manifest: %+v, %v", members, err)
	}
	if err := backend.VerifyManifest(t.Context(), object.Team, object.Digest, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("manifest verification: %v", err)
	}
}
