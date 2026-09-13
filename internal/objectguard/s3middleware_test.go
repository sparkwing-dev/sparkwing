package objectguard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

func bucketThatFails(t *testing.T, failFirst int64) (endpoint string, hits *atomic.Int64) {
	t.Helper()
	hits = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= failFirst {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`<Error><Code>InternalError</Code><Message>fake bucket is down</Message></Error>`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, hits
}

func guardedClient(t *testing.T, endpoint string, l *objectguard.Limiter, maxAttempts int) *awss3.Client {
	t.Helper()
	return awss3.New(awss3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		Retryer: retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = maxAttempts
			o.MaxBackoff = time.Millisecond
		}),
		EndpointResolverV2: awss3.NewDefaultEndpointResolverV2(),
	}, objectguard.WithBudget(l))
}

func classState(t *testing.T, l *objectguard.Limiter, c objectguard.Class) objectguard.ClassState {
	t.Helper()
	for _, cs := range l.State().Classes {
		if cs.Class == c {
			return cs
		}
	}
	t.Fatalf("the limiter reports no state for class %q", c)
	return objectguard.ClassState{}
}

// TestBudgetSpendsOneUnitPerBilledAttempt is the arithmetic the budget
// rests on: the store bills each attempt the retryer sends, so the
// budget has to count attempts, not API calls.
func TestBudgetSpendsOneUnitPerBilledAttempt(t *testing.T) {
	endpoint, hits := bucketThatFails(t, 3)
	l := objectguard.New(testConfig(100, 0))
	client := guardedClient(t, endpoint, l, 4)

	_, err := client.PutObject(context.Background(), &awss3.PutObjectInput{
		Bucket: aws.String("b"),
		Key:    aws.String("k"),
		Body:   strings.NewReader("payload"),
	})
	if err != nil {
		t.Fatalf("the put succeeded on its fourth attempt at the fake bucket, but returned: %v", err)
	}
	if got := hits.Load(); got != 4 {
		t.Fatalf("the fake bucket saw %d requests, want the 4 attempts the retryer sent", got)
	}
	if got := classState(t, l, objectguard.ClassPut).Allowed; got != 4 {
		t.Fatalf("the budget spent %d units for one put the store billed 4 times", got)
	}
}

func TestBudgetRefusesMidRetryAndTheRetryerStops(t *testing.T) {
	endpoint, hits := bucketThatFails(t, 100)
	l := objectguard.New(testConfig(2, 0))
	client := guardedClient(t, endpoint, l, 8)

	_, err := client.PutObject(context.Background(), &awss3.PutObjectInput{
		Bucket: aws.String("b"),
		Key:    aws.String("k"),
		Body:   strings.NewReader("payload"),
	})
	if err == nil {
		t.Fatal("a put against a bucket that fails every request returned no error")
	}
	if !strings.Contains(err.Error(), "object-store put budget exceeded") {
		t.Fatalf("the error does not name the budget: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("the fake bucket saw %d requests, want the 2 the budget allowed", got)
	}
	if got := classState(t, l, objectguard.ClassPut).Refused; got != 1 {
		t.Fatalf("the budget recorded %d refusals, want the single one that ended the retry loop", got)
	}
}

func TestBudgetClassesFollowTheOperation(t *testing.T) {
	endpoint, _ := bucketThatFails(t, 0)
	l := objectguard.New(testConfig(100, 0))
	client := guardedClient(t, endpoint, l, 1)
	ctx := context.Background()

	_, _ = client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String("b"), Key: aws.String("k")})
	_, _ = client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String("b"), Key: aws.String("k")})
	_, _ = client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: aws.String("b")})
	_, _ = client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String("b"), Key: aws.String("k")})
	_, _ = client.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("b"), Key: aws.String("k"), Body: strings.NewReader("x")})

	for class, want := range map[objectguard.Class]uint64{
		objectguard.ClassGet:    2,
		objectguard.ClassList:   1,
		objectguard.ClassDelete: 1,
		objectguard.ClassPut:    1,
	} {
		if got := classState(t, l, class).Allowed; got != want {
			t.Errorf("class %q spent %d units, want %d", class, got, want)
		}
	}
}

func TestClassForOperationBillsAnUnknownOperationAsAWrite(t *testing.T) {
	if got := objectguard.ClassForOperation("CreateMultipartUpload"); got != objectguard.ClassPut {
		t.Fatalf("an unrecognized operation billed as %q, want the expensive guess %q", got, objectguard.ClassPut)
	}
}

// TestBudgetBoundsAHotLoopAgainstAFailingBucket states the bound over a
// fixed wall-clock window: a caller that never stops retrying reaches the
// store at most its per-minute budget of times, however long it spins.
func TestBudgetBoundsAHotLoopAgainstAFailingBucket(t *testing.T) {
	const budget = 20
	endpoint, hits := bucketThatFails(t, 1<<30)
	l := objectguard.New(testConfig(budget, 0))
	client := guardedClient(t, endpoint, l, 1)

	ctx := context.Background()
	calls := 0
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		_, _ = client.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: aws.String("b"), Key: aws.String("k"), Body: strings.NewReader("x"),
		})
		calls++
	}
	if calls <= budget {
		t.Fatalf("the loop only spun %d times, which is too few to prove the budget bound", calls)
	}
	if got := hits.Load(); got != budget {
		t.Fatalf("%d requests reached the bucket over the window, want the budget of %d", got, budget)
	}
}
