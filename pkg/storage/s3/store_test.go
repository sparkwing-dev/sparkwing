package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
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

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
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

func TestArtifactStore_RoundTrip(t *testing.T) {
	t.Parallel()
	client := fakeS3(t)

	s := NewArtifactStore(testBucket, "cache", client)
	ctx := context.Background()

	if err := s.PutIfAbsent(ctx, "abcd1234", bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, err := s.Get(ctx, "abcd1234")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "hello" {
		t.Fatalf("Get = %q", got)
	}

	if _, err := s.Get(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}

}

func TestArtifactStore_PrefixIsolation(t *testing.T) {
	t.Parallel()
	client := fakeS3(t)

	a := NewArtifactStore(testBucket, "cache", client)
	b := NewArtifactStore(testBucket, "other", client)
	ctx := context.Background()

	if err := a.PutIfAbsent(ctx, "k", bytes.NewReader([]byte("A"))); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := b.PutIfAbsent(ctx, "k", bytes.NewReader([]byte("B"))); err != nil {
		t.Fatalf("Put b: %v", err)
	}
	rc, _ := a.Get(ctx, "k")
	gotA, _ := io.ReadAll(rc)
	rc.Close()
	rc, _ = b.Get(ctx, "k")
	gotB, _ := io.ReadAll(rc)
	rc.Close()
	if string(gotA) != "A" || string(gotB) != "B" {
		t.Fatalf("isolation broken: a=%q b=%q", gotA, gotB)
	}
}

func TestArtifactStore_List(t *testing.T) {
	t.Parallel()
	client := fakeS3(t)

	s := NewArtifactStore(testBucket, "cache", client)
	ctx := context.Background()

	put := func(key string) {
		t.Helper()
		if err := s.PutIfAbsent(ctx, key, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}
	put("runs/abc/state.ndjson")
	put("runs/def/state.ndjson")
	put("bin/some-key")

	got, err := s.List(ctx, "runs/")
	if err != nil {
		t.Fatalf("List runs/: %v", err)
	}
	want := map[string]bool{
		"runs/abc/state.ndjson": true,
		"runs/def/state.ndjson": true,
	}
	if len(got) != len(want) {
		t.Fatalf("List runs/ = %v, want %v", got, want)
	}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected key %q", k)
		}
	}

	other := NewArtifactStore(testBucket, "other", client)
	if err := other.PutIfAbsent(ctx, "runs/xyz/state.ndjson", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("Put other: %v", err)
	}
	got, _ = s.List(ctx, "runs/")
	for _, k := range got {
		if strings.Contains(k, "xyz") {
			t.Errorf("List leaked sibling-prefix key %q", k)
		}
	}
}

func TestArtifactStore_UsageTotalsStoredBytesAndObjects(t *testing.T) {
	t.Parallel()
	client := fakeS3(t)

	s := NewArtifactStore(testBucket, "cache", client)
	ctx := context.Background()

	usage, err := s.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage on an empty store: %v", err)
	}
	if usage.Bytes != 0 || usage.Objects != 0 {
		t.Fatalf("empty store measures %d bytes / %d objects, want 0/0", usage.Bytes, usage.Objects)
	}

	for key, body := range map[string]string{"a": "hello", "b": "worldly"} {
		if err := s.PutIfAbsent(ctx, key, strings.NewReader(body)); err != nil {
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

func TestArtifactStore_UsageIgnoresAnotherPrefix(t *testing.T) {
	t.Parallel()
	client := fakeS3(t)

	ctx := context.Background()
	mine := NewArtifactStore(testBucket, "mine", client)
	theirs := NewArtifactStore(testBucket, "theirs", client)
	if err := mine.PutIfAbsent(ctx, "k", strings.NewReader("1234")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := theirs.PutIfAbsent(ctx, "k", strings.NewReader("123456789")); err != nil {
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

func TestArtifactStore_UsageStopsAtItsPageCapAndSaysSo(t *testing.T) {
	t.Parallel()
	bucket := &endlessBucket{}
	s := NewArtifactStore(testBucket, "cache", bucket)
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

func TestArtifactStore_UsageStopsOnACancelledContext(t *testing.T) {
	t.Parallel()
	client := fakeS3(t)

	s := NewArtifactStore(testBucket, "cache", client)
	if err := s.PutIfAbsent(context.Background(), "a", strings.NewReader("x")); err != nil {
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
