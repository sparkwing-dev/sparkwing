package s3

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// ignorePreconditionsAPI is an S3 endpoint that accepts the
// conditional-write headers and silently ignores them: every PutObject
// succeeds regardless of If-None-Match / If-Match. It models an
// S3-compatible gateway without real CAS support.
type ignorePreconditionsAPI struct {
	API
	puts atomic.Int64
}

func (a *ignorePreconditionsAPI) PutObject(_ context.Context, _ *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	a.puts.Add(1)
	etag := "\"ignored\""
	return &s3.PutObjectOutput{ETag: &etag}, nil
}

func (a *ignorePreconditionsAPI) DeleteObject(_ context.Context, _ *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return &s3.DeleteObjectOutput{}, nil
}

func TestConditionalWritesSupported_FalseWhenEndpointIgnoresPreconditions(t *testing.T) {
	t.Parallel()
	api := &ignorePreconditionsAPI{}
	s := NewArtifactStore(testBucket, "cache", api)
	ctx := context.Background()

	supported, err := s.ConditionalWritesSupported(ctx)
	if err != nil {
		t.Fatalf("ConditionalWritesSupported: %v", err)
	}
	if supported {
		t.Fatalf("supported = true for an endpoint that ignores preconditions")
	}
}

func TestConditionalWritesSupported_MemoizesProbe(t *testing.T) {
	t.Parallel()
	api := &ignorePreconditionsAPI{}
	s := NewArtifactStore(testBucket, "cache", api)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := s.ConditionalWritesSupported(ctx); err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
	}
	if got := api.puts.Load(); got != 2 {
		t.Fatalf("PutObject called %d times across 3 capability checks, want 2 (probe runs once)", got)
	}
}

func TestConditionalWritesSupported_TrueAgainstFakeS3(t *testing.T) {
	t.Parallel()
	client, closer := fakeS3(t)
	defer closer()

	s := NewArtifactStore(testBucket, "cache", client)
	supported, err := s.ConditionalWritesSupported(context.Background())
	if err != nil {
		t.Fatalf("ConditionalWritesSupported: %v", err)
	}
	if !supported {
		t.Fatalf("supported = false against an endpoint that enforces preconditions")
	}
}

func TestConditional_ReportsStaticCapability(t *testing.T) {
	t.Parallel()
	var store storage.ArtifactStore = NewArtifactStore(testBucket, "cache", &ignorePreconditionsAPI{})
	if _, ok := storage.Conditional(store); !ok {
		t.Fatalf("Conditional reported the s3 ArtifactStore as non-conditional")
	}
}

func TestPutIfAbsent_MapsPreconditionFailed(t *testing.T) {
	t.Parallel()
	client, closer := fakeS3(t)
	defer closer()

	s := NewArtifactStore(testBucket, "cache", client)
	ctx := context.Background()

	if _, err := s.PutIfAbsent(ctx, "k", strings.NewReader("first")); err != nil {
		t.Fatalf("first PutIfAbsent: %v", err)
	}
	_, err := s.PutIfAbsent(ctx, "k", strings.NewReader("second"))
	if !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Fatalf("second PutIfAbsent: err = %v, want ErrPreconditionFailed", err)
	}
}
