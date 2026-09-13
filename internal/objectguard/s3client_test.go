package objectguard_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

type failingBucket struct {
	puts, gets, heads, lists, deletes atomic.Int64
}

var errBucketDown = errors.New("fake bucket: permanently unavailable")

func (f *failingBucket) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.puts.Add(1)
	return nil, errBucketDown
}

func (f *failingBucket) GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.gets.Add(1)
	return nil, errBucketDown
}

func (f *failingBucket) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.heads.Add(1)
	return nil, errBucketDown
}

func (f *failingBucket) DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deletes.Add(1)
	return nil, errBucketDown
}

func (f *failingBucket) DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	f.deletes.Add(1)
	return nil, errBucketDown
}

func (f *failingBucket) ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.lists.Add(1)
	return nil, errBucketDown
}

// TestGuardedClientBoundsAHotLoopAgainstAFailingBucket is the ticket's
// arithmetic: a caller that never stops retrying reaches the store at
// most its per-minute budget of times, however long it spins.
func TestGuardedClientBoundsAHotLoopAgainstAFailingBucket(t *testing.T) {
	const budget = 25
	bucket := &failingBucket{}
	cfg := testConfig(budget, 0)
	client := objectguard.GuardS3(objectguard.New(cfg), bucket)

	ctx := context.Background()
	deadline := time.Now().Add(250 * time.Millisecond)
	attempts := 0
	for time.Now().Before(deadline) {
		_, _ = client.PutObject(ctx, &s3.PutObjectInput{})
		_, _ = client.GetObject(ctx, &s3.GetObjectInput{})
		_, _ = client.HeadObject(ctx, &s3.HeadObjectInput{})
		_, _ = client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{})
		_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{})
		attempts++
	}
	if attempts <= budget {
		t.Fatalf("the loop only spun %d times, which is too few to prove the budget bound", attempts)
	}
	if got := bucket.puts.Load(); got != budget {
		t.Fatalf("%d puts reached the bucket over the window, want the budget of %d", got, budget)
	}
	if got := bucket.gets.Load() + bucket.heads.Load(); got != budget {
		t.Fatalf("%d get-class requests reached the bucket, want the budget of %d", got, budget)
	}
	if got := bucket.lists.Load(); got != budget {
		t.Fatalf("%d lists reached the bucket, want the budget of %d", got, budget)
	}
	if got := bucket.deletes.Load(); got != budget {
		t.Fatalf("%d deletes reached the bucket, want the budget of %d", got, budget)
	}
}

func TestGuardedClientRefusalNamesTheClassAndBudget(t *testing.T) {
	bucket := &failingBucket{}
	client := objectguard.GuardS3(objectguard.New(testConfig(1, 0)), bucket)
	ctx := context.Background()
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{}); !errors.Is(err, errBucketDown) {
		t.Fatalf("the first put did not reach the bucket: %v", err)
	}
	_, err := client.PutObject(ctx, &s3.PutObjectInput{})
	var budget *objectguard.BudgetError
	if !errors.As(err, &budget) {
		t.Fatalf("the refused put returned %T, want *BudgetError", err)
	}
	if budget.Class != objectguard.ClassPut {
		t.Fatalf("the refusal names class %q, want put", budget.Class)
	}
	if bucket.puts.Load() != 1 {
		t.Fatalf("%d puts reached the bucket, want the single one inside budget", bucket.puts.Load())
	}
}
