package s3

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// An S3-compatible store named by hostname is addressed path-style, because
// such a store rarely serves each bucket as a subdomain.
func TestNewClientHonoursTheAWSEndpointVariables(t *testing.T) {
	for _, name := range []string{"AWS_ENDPOINT_URL_S3", "AWS_ENDPOINT_URL"} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Scheme+"://"+r.URL.Host+r.URL.Path)
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			})
			t.Setenv("AWS_ENDPOINT_URL_S3", "")
			t.Setenv("AWS_ENDPOINT_URL", "")
			t.Setenv(name, "http://bucket.invalid")
			t.Setenv("AWS_REGION", "us-east-1")
			t.Setenv("AWS_ACCESS_KEY_ID", "AKID")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRET")
			t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/none")
			t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/none")
			client, err := NewClient(context.Background(), func(o *awss3.Options) { o.HTTPClient = s3HandlerClient{handler} })
			if err != nil {
				t.Fatal(err)
			}
			_, _ = client.HeadBucket(context.Background(), &awss3.HeadBucketInput{Bucket: aws.String("probe-bucket")})
			mu.Lock()
			defer mu.Unlock()
			if len(paths) == 0 {
				t.Fatalf("%s=%s reached no request; the client went elsewhere", name, "http://bucket.invalid")
			}
			if paths[0] != "http://bucket.invalid/probe-bucket" {
				t.Fatalf("request %q, want the configured endpoint and path-style bucket", paths[0])
			}
		})
	}
}
