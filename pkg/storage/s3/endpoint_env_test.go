package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
)

// An S3-compatible store named by hostname is addressed path-style, because
// such a store rarely serves each bucket as a subdomain.
func TestNewClientHonoursTheAWSEndpointVariables(t *testing.T) {
	for _, name := range []string{"AWS_ENDPOINT_URL_S3", "AWS_ENDPOINT_URL"} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.Host+r.URL.Path)
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(srv.Close)
			t.Setenv("AWS_ENDPOINT_URL_S3", "")
			t.Setenv("AWS_ENDPOINT_URL", "")
			t.Setenv(name, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1))
			t.Setenv("AWS_REGION", "us-east-1")
			t.Setenv("AWS_ACCESS_KEY_ID", "AKID")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRET")
			t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/none")
			t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/none")
			client, err := s3store.NewClient(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_, _ = client.HeadBucket(context.Background(), &awss3.HeadBucketInput{Bucket: aws.String("probe-bucket")})
			mu.Lock()
			defer mu.Unlock()
			if len(paths) == 0 {
				t.Fatalf("%s=%s reached no request; the client went elsewhere", name, srv.URL)
			}
			if !strings.Contains(paths[0], "/probe-bucket") {
				t.Fatalf("request %q, want a path-style request naming the bucket", paths[0])
			}
		})
	}
}
