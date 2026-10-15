package blob_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestS3ManifestRequiresCompleteClosedBytes(t *testing.T) {
	body := `{"entries":[]}`
	closeErr := errors.New("close failed")
	for _, tc := range []struct {
		name, payload string
		length        int64
		closeErr      error
		cancel        bool
		want          error
	}{
		{name: "verified", payload: body, length: int64(len(body))},
		{name: "corrupt", payload: `{"entries":{}}`, length: int64(len(body)), want: blob.ErrIntegrity},
		{name: "short", payload: body[:len(body)-1], length: int64(len(body)), want: blob.ErrIntegrity},
		{name: "long", payload: body + " ", length: int64(len(body)), want: blob.ErrIntegrity},
		{name: "length", payload: body, length: int64(len(body)) + 1, want: blob.ErrIntegrity},
		{name: "close", payload: body, length: int64(len(body)), closeErr: closeErr, want: closeErr},
		{name: "canceled response", payload: body, length: int64(len(body)), cancel: true, want: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			closes := 0
			client := s3.New(s3.Options{
				Region: "us-east-1", BaseEndpoint: aws.String("https://objects.invalid"), UsePathStyle: true, RetryMaxAttempts: 1,
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
				}),
				HTTPClient: s3HTTP(func(r *http.Request) (*http.Response, error) {
					if r.Method != http.MethodGet || r.URL.Path != "/bucket/teams/team/cas/"+string(digest(body)) {
						t.Fatalf("manifest read escaped its key: %s %s", r.Method, r.URL.Path)
					}
					if tc.cancel {
						cancel()
					}
					return &http.Response{
						StatusCode: http.StatusOK, Header: http.Header{"Content-Length": {fmt.Sprint(tc.length)}}, ContentLength: tc.length,
						Body: closeCounter{Reader: strings.NewReader(tc.payload), closeErr: tc.closeErr, count: &closes}, Request: r,
					}, nil
				}),
			})
			objects, err := blob.NewS3(client, "bucket")
			if err != nil {
				t.Fatal(err)
			}
			members, err := objects.ReadManifest(ctx, "team", digest(body))
			if tc.want == nil {
				if err != nil || members == nil || len(members) != 0 {
					t.Fatalf("empty manifest: %+v: %v", members, err)
				}
			} else if members != nil || !errors.Is(err, tc.want) {
				t.Fatalf("read: %+v: %v, want %v", members, err, tc.want)
			}
			if closes != 1 {
				t.Fatalf("closed %d streams, want one", closes)
			}
		})
	}
}
