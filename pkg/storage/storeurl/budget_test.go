package storeurl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	t.Setenv("SPARKWING_S3_ENDPOINT", endpoint)
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
	sharedLimiter = func() (*objectguard.Limiter, error) { return l, nil }
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
	if got := hits.Load(); got != int64(SDKMaxAttempts) {
		t.Fatalf("the bucket was asked %d times for one put, want the cap of %d", got, SDKMaxAttempts)
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
		"SPARKWING_OBJECT_STORE_BREAKER=off",
		"SPARKWING_OBJECT_STORE_PUT_PER_MINUTE",
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

// TestTrippedBudgetFailsARunsStateWriteClosed is the failure a run's exit
// code comes from: FinishRun is what tells the orchestrator a run's
// terminal state reached the object store, and a spent budget makes it
// say no, in words, rather than queueing the write behind the outbox.
func TestTrippedBudgetFailsARunsStateWriteClosed(t *testing.T) {
	endpoint, _ := countingBucket(t, http.StatusOK)
	s3Env(t, endpoint)
	t.Setenv("SPARKWING_HOME", t.TempDir())

	tight := objectguard.Config{Enabled: true, Reset: objectguard.TripResetDay, Limits: map[objectguard.Class]objectguard.Limit{
		objectguard.ClassPut: {PerMinute: 1},
	}}
	useLimiter(t, objectguard.New(tight))

	ctx := context.Background()
	art, err := OpenArtifactStore(ctx, "s3://bucket/prefix")
	if err != nil {
		t.Fatalf("open artifact store: %v", err)
	}
	if err := art.Put(ctx, "warm", strings.NewReader("payload")); err != nil {
		t.Fatalf("the put inside the budget failed: %v", err)
	}

	state, err := OpenStateStoreFromSpec(ctx, backends.Spec{
		Type:   backends.TypeS3,
		Bucket: "bucket",
		Prefix: "state",
	}, nil)
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	defer func() { _ = state.Close() }()

	if err := state.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "p", StartedAt: time.Now().UTC()}); err != nil {
		if !errors.Is(err, objectguard.ErrBudgetExceeded) {
			t.Fatalf("CreateRun: %v", err)
		}
		return
	}
	err = state.FinishRun(ctx, "run-1", "success", "")
	if err == nil {
		t.Fatal("a run finished successfully with its terminal state refused by the budget")
	}
	if !errors.Is(err, objectguard.ErrBudgetExceeded) {
		t.Fatalf("the run's failure does not match ErrBudgetExceeded, so it queued instead of failing closed: %v", err)
	}
	if strings.Contains(err.Error(), "queued in the local outbox") {
		t.Fatalf("a refused write was staged to the outbox instead of failing closed: %v", err)
	}
	for _, want := range []string{"object-store put budget exceeded", "SPARKWING_OBJECT_STORE_BREAKER=off"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the run's failure does not mention %q:\n%v", want, err)
		}
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
