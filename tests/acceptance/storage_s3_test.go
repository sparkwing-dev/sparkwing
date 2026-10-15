package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

type collectionS3 struct{ objects map[string][]byte }

func (s collectionS3) RoundTrip(r *http.Request) (*http.Response, error) {
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	out := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: r}
	switch r.Method {
	case http.MethodGet:
		body, found := s.objects[key]
		if !found {
			out.StatusCode = http.StatusNotFound
			break
		}
		out.ContentLength = int64(len(body))
		out.Header.Set("Content-Length", strconv.Itoa(len(body)))
		out.Header.Set("ETag", fmt.Sprintf(`"%x"`, sha256.Sum256(body)))
		out.Body = io.NopCloser(bytes.NewReader(body))
	case http.MethodPut:
		if source := r.Header.Get("X-Amz-Copy-Source"); source != "" {
			source, err := url.PathUnescape(source)
			if err != nil {
				return nil, err
			}
			body, found := s.objects[strings.TrimPrefix(source, "bucket/")]
			if _, exists := s.objects[key]; !found || exists || r.Header.Get("If-None-Match") != "*" || r.Header.Get("X-Amz-Copy-Source-If-Match") != fmt.Sprintf(`"%x"`, sha256.Sum256(body)) {
				out.StatusCode = http.StatusPreconditionFailed
				break
			}
			s.objects[key] = bytes.Clone(body)
			out.Body = io.NopCloser(strings.NewReader(`<CopyObjectResult><ETag>"final"</ETag></CopyObjectResult>`))
		} else {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(body)
			if r.ContentLength != int64(len(body)) || r.Header.Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
				out.StatusCode = http.StatusBadRequest
				break
			}
			s.objects[key] = bytes.Clone(body)
		}
	case http.MethodDelete:
		delete(s.objects, key)
	default:
		return nil, fmt.Errorf("unexpected S3 request: %s %s", r.Method, r.URL)
	}
	return out, nil
}

func checkStorageS3Collection(t *testing.T) {
	transport := collectionS3{objects: make(map[string][]byte)}
	client := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String("https://objects.invalid"), UsePathStyle: true, RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
		}),
		HTTPClient: &http.Client{Transport: transport},
	})
	objects, err := blob.NewS3(client, "bucket")
	if err != nil {
		t.Fatal(err)
	}
	if err := acceptance.CheckBlobCollection(t.Context(), t.TempDir(), objects, blob.TransferClient{Transport: transport}); err != nil {
		t.Fatal(err)
	}
	if len(transport.objects) != 0 {
		t.Fatalf("collection leaked %d objects", len(transport.objects))
	}
}
