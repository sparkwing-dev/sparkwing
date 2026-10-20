package s3

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

const testBucket = "sparkwing-test"

type s3HandlerClient struct{ http.Handler }

func (c s3HandlerClient) Do(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if r.ContentLength >= 0 {
		r.Header.Set("Content-Length", strconv.FormatInt(r.ContentLength, 10))
	}
	if r.Body == nil {
		r.Body = http.NoBody
	}
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	_ = r.Body.Close()
	return w.Result(), nil
}

func fakeS3(t *testing.T) *awss3.Client {
	t.Helper()
	backend := s3mem.New()
	faker := gofakes3.New(backend)

	client := awss3.New(awss3.Options{
		Region:             "us-east-1",
		BaseEndpoint:       aws.String("http://bucket.invalid"),
		HTTPClient:         s3HandlerClient{faker.Server()},
		UsePathStyle:       true,
		Credentials:        credentials.NewStaticCredentialsProvider("test", "test", ""),
		EndpointResolverV2: awss3.NewDefaultEndpointResolverV2(),
	})

	if _, err := client.CreateBucket(context.Background(), &awss3.CreateBucketInput{
		Bucket: aws.String(testBucket),
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return client
}

func TestMeasurementStore_UsageTotalsStoredBytesAndObjects(t *testing.T) {
	t.Parallel()
	client := fakeS3(t)

	s := NewMeasurementStore(testBucket, "cache", client)
	ctx := context.Background()

	usage, err := s.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage on an empty store: %v", err)
	}
	if usage.Bytes != 0 || usage.Objects != 0 {
		t.Fatalf("empty store measures %d bytes / %d objects, want 0/0", usage.Bytes, usage.Objects)
	}

	for key, body := range map[string]string{"a": "hello", "b": "worldly"} {
		if err := putMeasurement(ctx, client, "cache/"+key, body); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}

	usage, err = s.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.Objects != 2 {
		t.Errorf("measured %d objects, want 2", usage.Objects)
	}
	if want := int64(len("hello") + len("worldly")); usage.Bytes != want {
		t.Errorf("measured %d bytes, want %d", usage.Bytes, want)
	}
	if usage.ObservedAt.IsZero() {
		t.Error("a measurement carries no observation time")
	}
}

func TestMeasurementStore_UsageIgnoresAnotherPrefix(t *testing.T) {
	t.Parallel()
	client := fakeS3(t)

	ctx := context.Background()
	mine := NewMeasurementStore(testBucket, "mine", client)
	if err := putMeasurement(ctx, client, "mine/k", "1234"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := putMeasurement(ctx, client, "theirs/k", "123456789"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	usage, err := mine.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.Objects != 1 || usage.Bytes != 4 {
		t.Errorf("prefixed store measures %d bytes / %d objects, want 4/1", usage.Bytes, usage.Objects)
	}
}

// safety: a bucket larger than the page cap looks exactly like this, and no
// fake that terminates could exercise the cap.
type endlessBucket struct {
	API
	pages atomic.Int64
}

func (b *endlessBucket) ListObjectsV2(
	context.Context, *awss3.ListObjectsV2Input, ...func(*awss3.Options),
) (*awss3.ListObjectsV2Output, error) {
	b.pages.Add(1)
	truncated := true
	token := "next"
	size := int64(4)
	return &awss3.ListObjectsV2Output{
		Contents:              []types.Object{{Key: aws.String("k"), Size: &size}},
		IsTruncated:           &truncated,
		NextContinuationToken: &token,
	}, nil
}

func TestMeasurementStore_UsageStopsAtItsPageCapAndSaysSo(t *testing.T) {
	t.Parallel()
	bucket := &endlessBucket{}
	s := NewMeasurementStore(testBucket, "cache", bucket)
	s.MaxUsagePages = 3

	usage, err := s.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if !usage.Partial {
		t.Error("a measurement that stopped at its page cap reported a complete total")
	}
	if got := bucket.pages.Load(); got != 3 {
		t.Errorf("the measurement spent %d listings against a cap of 3", got)
	}
	if usage.Objects != 3 {
		t.Errorf("the partial total carries %d objects, want the 3 it counted", usage.Objects)
	}
}

func TestMeasurementStore_UsageStopsOnACancelledContext(t *testing.T) {
	t.Parallel()
	client := fakeS3(t)

	s := NewMeasurementStore(testBucket, "cache", client)
	if err := putMeasurement(context.Background(), client, "cache/a", "x"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	usage, err := s.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage on a cancelled context returned an error rather than a partial total: %v", err)
	}
	if !usage.Partial {
		t.Error("a measurement cut short by its deadline reported a complete total")
	}
}

func putMeasurement(ctx context.Context, client *awss3.Client, key, body string) error {
	_, err := client.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(testBucket), Key: aws.String(key), Body: strings.NewReader(body)})
	return err
}

type prefixScopedBucket struct {
	API
	allowed string
	seen    []string
}

func (b *prefixScopedBucket) ListObjectsV2(ctx context.Context, in *awss3.ListObjectsV2Input, opts ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	prefix := aws.ToString(in.Prefix)
	b.seen = append(b.seen, prefix)
	if !strings.HasPrefix(prefix, b.allowed) {
		return nil, &types.NoSuchKey{Message: aws.String("AccessDenied: s3:ListBucket is scoped to " + b.allowed)}
	}
	return b.API.ListObjectsV2(ctx, in, opts...)
}

func TestMeasurementStore_ListsOnlyUnderAPrefixScopedPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, configured := range []string{"artifacts", "artifacts/", "/artifacts/"} {
		client := fakeS3(t)
		if err := putMeasurement(ctx, client, "artifacts/k", "1234"); err != nil {
			t.Fatal(err)
		}
		if err := putMeasurement(ctx, client, "artifacts-other/k", "123456789"); err != nil {
			t.Fatal(err)
		}
		scoped := &prefixScopedBucket{API: client, allowed: "artifacts/"}
		usage, err := NewMeasurementStore(testBucket, configured, scoped).Usage(ctx)
		if err != nil {
			t.Fatalf("prefix %q under an artifacts/-scoped policy: %v (listed %q)", configured, err, scoped.seen)
		}
		if usage.Objects != 1 || usage.Bytes != 4 {
			t.Errorf("prefix %q measures %d bytes / %d objects, want 4/1", configured, usage.Bytes, usage.Objects)
		}
	}
}
