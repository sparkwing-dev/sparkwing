package storeurl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
)

func s3Env(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/credentials")
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/config")
	t.Setenv("AWS_ENDPOINT_URL_S3", endpoint)
}

func countingBucket(t *testing.T, status int) (endpoint string, hits *atomic.Int64) {
	t.Helper()
	hits = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if status >= 400 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`<Error><Code>InternalError</Code><Message>fake bucket is down</Message></Error>`))
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, hits
}

func useLimiter(t *testing.T, l *objectguard.Limiter) {
	t.Helper()
	prior := sharedLimiter
	sharedLimiter = func() *objectguard.Limiter { return l }
	t.Cleanup(func() { sharedLimiter = prior })
}

// TestOpenArtifactStoreCapsTheSDKRetryer proves SDKMaxAttempts reaches
// the client the factory hands out: one failing call bills that many
// requests and no more.
func TestOpenArtifactStoreCapsTheSDKRetryer(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 5.5s of real work; the fast class runs under -short")
	}
	endpoint, hits := countingBucket(t, http.StatusInternalServerError)
	s3Env(t, endpoint)
	useLimiter(t, objectguard.New(objectguard.DefaultConfig()))

	store, err := OpenArtifactStore(context.Background(), "s3://bucket/prefix")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Put(context.Background(), "key", strings.NewReader("payload")); err == nil {
		t.Fatal("a put against a bucket that answers 500 returned no error")
	}
	if got := hits.Load(); got != int64(s3store.SDKMaxAttempts) {
		t.Fatalf("the bucket was asked %d times for one put, want the cap of %d", got, s3store.SDKMaxAttempts)
	}
}

// TestTrippedBudgetSurfacesAReadableStoreError is what a run's operator
// sees: the failure names the class, says the budget is this process's,
// and names both ways out.
func TestTrippedBudgetSurfacesAReadableStoreError(t *testing.T) {
	endpoint, hits := countingBucket(t, http.StatusOK)
	s3Env(t, endpoint)

	tight := objectguard.Config{Enabled: true, Reset: objectguard.TripResetDay, Limits: map[objectguard.Class]objectguard.Limit{
		objectguard.ClassPut: {PerMinute: 1},
	}}
	useLimiter(t, objectguard.New(tight))

	ctx := context.Background()
	store, err := OpenArtifactStore(ctx, "s3://bucket/prefix")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Put(ctx, "first", strings.NewReader("payload")); err != nil {
		t.Fatalf("the put inside the budget failed: %v", err)
	}

	err = store.Put(ctx, "second", strings.NewReader("payload"))
	if err == nil {
		t.Fatal("the put past the budget succeeded")
	}
	if !errors.Is(err, objectguard.ErrBudgetExceeded) {
		t.Fatalf("the store error no longer matches ErrBudgetExceeded: %v", err)
	}
	for _, want := range []string{
		"s3 put second",
		"object-store put budget exceeded",
		"belongs to this process alone",
		"--object-store-budget put:minute",
		"sparkwing cluster object-store reset-breaker",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the store error does not mention %q:\n%v", want, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d requests reached the bucket, want the single one inside the budget", got)
	}
}

func TestMeasurementStoreStaysOutsideTheListBudget(t *testing.T) {
	hits := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`))
	}))
	t.Cleanup(srv.Close)
	s3Env(t, srv.URL)

	spent := objectguard.New(objectguard.Config{
		Enabled: true,
		Limits:  map[objectguard.Class]objectguard.Limit{objectguard.ClassList: {PerMinute: 1}},
	})
	if err := spent.Allow(objectguard.ClassList); err != nil {
		t.Fatalf("seed the list budget: %v", err)
	}
	useLimiter(t, spent)

	budgeted, err := OpenArtifactStore(context.Background(), "s3://bucket/prefix")
	if err != nil {
		t.Fatalf("OpenArtifactStore: %v", err)
	}
	if _, err := budgeted.List(context.Background(), ""); err == nil {
		t.Error("an ordinary client listed past a spent list budget")
	}

	measured, err := OpenMeasurementStore(context.Background(), "s3://bucket/prefix", 0)
	if err != nil {
		t.Fatalf("OpenMeasurementStore: %v", err)
	}
	usage, ok, err := storage.Usage(context.Background(), measured)
	if err != nil {
		t.Fatalf("the measurement was refused by the list budget: %v", err)
	}
	if !ok {
		t.Fatal("the measurement store cannot total itself")
	}
	if usage.Objects != 0 {
		t.Errorf("measured %d objects from an empty listing, want 0", usage.Objects)
	}
	if hits.Load() == 0 {
		t.Error("the measurement never reached the bucket")
	}
}
