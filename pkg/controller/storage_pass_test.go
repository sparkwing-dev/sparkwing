package controller_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const passBucket = "sparkwing-data"

type listHook struct {
	*s3.Client
	mu    sync.Mutex
	after func(prefix string)
}

func (h *listHook) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, o ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	out, err := h.Client.ListObjectsV2(ctx, in, o...)
	h.mu.Lock()
	after := h.after
	h.mu.Unlock()
	if after != nil && in.Delimiter == nil {
		after(aws.ToString(in.Prefix))
	}
	return out, err
}

type passBuckets struct {
	raw         *s3.Client
	hook        *listHook
	cache, logs *teamblob.Store
	now         time.Time
}

func newPassBuckets(t *testing.T) *passBuckets {
	t.Helper()
	fake := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(fake.Close)
	raw := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(fake.URL), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	if _, err := raw.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(passBucket)}); err != nil {
		t.Fatal(err)
	}
	b := &passBuckets{raw: raw, hook: &listHook{Client: raw}, now: time.Now()}
	open := func(prefix string, maxAge func(string) time.Duration) *teamblob.Store {
		s, err := teamblob.New(teamblob.Options{
			Bucket: passBucket, Prefix: prefix, Client: b.hook, TeamObjectMaxAge: maxAge,
			Now: func() time.Time { return b.now },
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	b.cache = open("cache", controller.CacheObjectMaxAge)
	b.logs = open("logs", nil)
	return b
}

func (b *passBuckets) put(t *testing.T, key string, n int) {
	t.Helper()
	if _, err := b.raw.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(passBucket), Key: aws.String(key), Body: strings.NewReader(strings.Repeat("x", n)),
	}); err != nil {
		t.Fatal(err)
	}
}

func (b *passBuckets) has(t *testing.T, key string) bool {
	t.Helper()
	_, err := b.raw.HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: aws.String(passBucket), Key: aws.String(key)})
	return err == nil
}

func held(t *testing.T, st *store.Store, team store.Team, kind store.StorageKind) int64 {
	t.Helper()
	all, err := st.TeamStorage(context.Background(), team)
	if err != nil {
		t.Fatal(err)
	}
	return all[kind].UsedBytes
}

func commitStorage(t *testing.T, st *store.Store, team store.Team, kind store.StorageKind, n int64) {
	t.Helper()
	ctx := context.Background()
	res, err := st.ReserveStorage(ctx, store.StorageReserve{Team: team, Kind: kind, Bytes: n})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CommitStorage(ctx, store.StorageCommit{ID: res.ID, Team: team, Kind: kind, Bytes: n}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceStoragePassPrunesOrphanButKeepsQueuedRun(t *testing.T) {
	for _, bound := range []bool{false, true} {
		name := "orphan"
		if bound {
			name = "queued"
		}
		t.Run(name, func(t *testing.T) {
			f := freeTierFixture(t, 5)
			b := newPassBuckets(t)
			f.srv.WithStoragePass(b.cache, b.logs)
			_ = freeTeamToken(t, f.store, "source-team")
			allowance := int64(1 << 30)
			if _, err := f.store.SetCreditSettings(t.Context(), store.CreditSettingsUpdate{StorageFreeAllowanceBytes: &allowance}); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-25 * time.Hour)
			key := "sources/" + strings.Repeat("a", 64) + "/" + strings.Repeat("1", 32)
			u, err := f.store.ReserveUpload(t.Context(), store.UploadRequest{
				Team: "source-team", Kind: store.StorageCache,
				Key: key, Size: 10, SHA256: strings.Repeat("a", 64), Principal: "owner", ClaimPrefix: "swu_owner", Provenance: "local", Now: old,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.CommitUpload(t.Context(), u.Team, u.ID, u.Principal, old.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if bound {
				tn, err := f.store.ForTeam(t.Context(), u.Team)
				if err != nil {
					t.Fatal(err)
				}
				if err := tn.CreateSourceTriggerWithRun(t.Context(), store.Trigger{ID: "run-source", Pipeline: "build", CreatedAt: old.Add(2 * time.Minute)},
					store.Run{ID: "run-source", Pipeline: "build", Status: "pending", CreatedAt: old.Add(2 * time.Minute), StartedAt: old.Add(2 * time.Minute)},
					key, "owner", "swu_owner"); err != nil {
					t.Fatal(err)
				}
				b.now = time.Now().Add(31 * 24 * time.Hour)
			}
			s3key := "cache/teams/source-team/local/" + key
			b.put(t, s3key, 10)
			if ran, err := f.srv.RunStoragePass(t.Context()); err != nil || !ran {
				t.Fatalf("pass = %v, %v", ran, err)
			}
			if got := b.has(t, s3key); got != bound {
				t.Fatalf("source in S3 = %t, want %t", got, bound)
			}
			wantBytes := int64(0)
			if bound {
				wantBytes = 10
			}
			if got := held(t, f.store, u.Team, store.StorageCache); got != wantBytes {
				t.Fatalf("source quota bytes = %d, want %d", got, wantBytes)
			}
		})
	}
}

// The storage pass replaces each team's count with what it lists in the
// cache's and the logs service's prefixes, keeps a write committed while it
// was listing, and drops a count the bucket no longer backs.
func TestTheStoragePassReconcilesAndKeepsWritesInFlight(t *testing.T) {
	f := freeTierFixture(t, 5)
	b := newPassBuckets(t)
	f.srv.WithStoragePass(b.cache, b.logs)
	for _, team := range []store.Team{"alpha", "beta"} {
		if code := f.trigger(freeTeamToken(t, f.store, team)); code != 202 {
			t.Fatalf("%s's run = %d", team, code)
		}
	}
	b.put(t, "cache/teams/alpha/bins/one", 300)
	b.put(t, "logs/teams/alpha/runs/r1/build.log", 40)
	commitStorage(t, f.store, "beta", store.StorageCache, 999)

	var once sync.Once
	b.hook.after = func(prefix string) {
		if prefix == "cache/teams/alpha/" {
			once.Do(func() { commitStorage(t, f.store, "alpha", store.StorageCache, 7) })
		}
	}
	ran, err := f.srv.RunStoragePass(context.Background())
	if err != nil || !ran {
		t.Fatalf("pass = %t, %v", ran, err)
	}
	if got := held(t, f.store, "alpha", store.StorageCache); got != 307 {
		t.Errorf("alpha's cache = %d, want the 300 listed plus the 7 committed during the listing", got)
	}
	if got := held(t, f.store, "alpha", store.StorageLogs); got != 40 {
		t.Errorf("alpha's logs = %d, want the 40 listed", got)
	}
	if got := held(t, f.store, "beta", store.StorageCache); got != 0 {
		t.Errorf("beta's cache = %d, want 0: the bucket holds none of it", got)
	}

	b.hook.after = nil
	if ran, err := f.srv.RunStoragePass(context.Background()); err != nil || ran {
		t.Fatalf("a second pass inside the window = %t, %v; want it skipped", ran, err)
	}
}

// The pass expires cache objects past their age in the listing it already
// makes, including the operator's team and token namespaces. Log retention
// remains with the logs service.
func TestTheStoragePassExpiresOldCacheObjects(t *testing.T) {
	f := freeTierFixture(t, 5)
	b := newPassBuckets(t)
	f.srv.WithStoragePass(b.cache, b.logs)
	if code := f.trigger(freeTeamToken(t, f.store, "alpha")); code != 202 {
		t.Fatal(code)
	}
	b.put(t, "cache/teams/alpha/bins/old", 300)
	b.put(t, "cache/teams/default/bins/operator", 10)
	b.put(t, "cache/bins/operator-root", 10)
	b.put(t, "logs/teams/alpha/runs/r1/build.log", 40)
	b.now = b.now.Add(controller.CacheObjectMaxAge("alpha") + time.Hour)

	if _, err := f.srv.RunStoragePass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.has(t, "cache/teams/alpha/bins/old") {
		t.Error("a cache object past its age survived the pass")
	}
	if b.has(t, "cache/teams/default/bins/operator") || b.has(t, "cache/bins/operator-root") {
		t.Error("old operator cache survived the pass")
	}
	if !b.has(t, "logs/teams/alpha/runs/r1/build.log") {
		t.Error("the cache pass expired a log")
	}
	if got := held(t, f.store, "alpha", store.StorageCache); got != 0 {
		t.Errorf("alpha's cache after expiry = %d, want 0", got)
	}
}

// A listing that fails leaves every count where it was rather than taking a
// partial listing for the whole, and says so in health.
func TestAFailedStoragePassKeepsTheCounts(t *testing.T) {
	f := freeTierFixture(t, 5)
	b := newPassBuckets(t)
	f.srv.WithStoragePass(b.cache, b.logs)
	if code := f.trigger(freeTeamToken(t, f.store, "alpha")); code != 202 {
		t.Fatal(code)
	}
	commitStorage(t, f.store, "alpha", store.StorageCache, 500)
	if _, err := b.raw.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String(passBucket)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.RunStoragePass(context.Background()); err == nil {
		t.Fatal("a pass over a missing bucket reported success")
	}
	if got := held(t, f.store, "alpha", store.StorageCache); got != 500 {
		t.Errorf("alpha's cache after a failed pass = %d, want the 500 it had", got)
	}
	var health struct {
		Problems []string `json:"problems"`
	}
	f.call("GET", "/api/v1/health", "", nil, &health)
	if !strings.Contains(strings.Join(health.Problems, "\n"), "storage pass") {
		t.Errorf("health problems = %v, want the failed storage pass", health.Problems)
	}
}
