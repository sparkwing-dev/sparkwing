package teamblob_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

const bucket = "teamblob-test"

// counting wraps a client and counts each call by operation, which is
// what the object store bills.
type counting struct {
	teamblob.Client
	mu     sync.Mutex
	calls  map[string]int
	failOn map[string]error
	// keepKey makes a batch delete report the matching keys as failed.
	keepKey func(key string) bool
	// afterList runs once a listing has answered, before the caller sees it.
	afterList func(in *s3.ListObjectsV2Input)
}

func (c *counting) note(op string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[op]++
	return c.failOn[op]
}

func (c *counting) count(op string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[op]
}

func (c *counting) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.calls {
		n += v
	}
	return n
}

func (c *counting) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = map[string]int{}
}

func (c *counting) GetObject(ctx context.Context, in *s3.GetObjectInput, o ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if err := c.note("GetObject"); err != nil {
		return nil, err
	}
	return c.Client.GetObject(ctx, in, o...)
}

func (c *counting) PutObject(ctx context.Context, in *s3.PutObjectInput, o ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if err := c.note("PutObject"); err != nil {
		return nil, err
	}
	return c.Client.PutObject(ctx, in, o...)
}

func (c *counting) HeadObject(ctx context.Context, in *s3.HeadObjectInput, o ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if err := c.note("HeadObject"); err != nil {
		return nil, err
	}
	return c.Client.HeadObject(ctx, in, o...)
}

func (c *counting) DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, o ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if err := c.note("DeleteObject"); err != nil {
		return nil, err
	}
	return c.Client.DeleteObject(ctx, in, o...)
}

func (c *counting) DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, o ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	if err := c.note("DeleteObjects"); err != nil {
		return nil, err
	}
	if c.keepKey == nil {
		return c.Client.DeleteObjects(ctx, in, o...)
	}
	var send []types.ObjectIdentifier
	var failed []types.Error
	for _, id := range in.Delete.Objects {
		if c.keepKey(aws.ToString(id.Key)) {
			failed = append(failed, types.Error{Key: id.Key, Code: aws.String("AccessDenied")})
		} else {
			send = append(send, id)
		}
	}
	out := &s3.DeleteObjectsOutput{}
	if len(send) > 0 {
		in.Delete.Objects = send
		res, err := c.Client.DeleteObjects(ctx, in, o...)
		if err != nil {
			return nil, err
		}
		out = res
	}
	out.Errors = append(out.Errors, failed...)
	return out, nil
}

func (c *counting) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, o ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if err := c.note("ListObjectsV2"); err != nil {
		return nil, err
	}
	out, err := c.Client.ListObjectsV2(ctx, in, o...)
	if c.afterList != nil {
		c.afterList(in)
	}
	return out, err
}

func (c *counting) CreateMultipartUpload(ctx context.Context, in *s3.CreateMultipartUploadInput, o ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	if err := c.note("CreateMultipartUpload"); err != nil {
		return nil, err
	}
	return c.Client.CreateMultipartUpload(ctx, in, o...)
}

func (c *counting) UploadPart(ctx context.Context, in *s3.UploadPartInput, o ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	if err := c.note("UploadPart"); err != nil {
		return nil, err
	}
	return c.Client.UploadPart(ctx, in, o...)
}

func (c *counting) CompleteMultipartUpload(ctx context.Context, in *s3.CompleteMultipartUploadInput, o ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	if err := c.note("CompleteMultipartUpload"); err != nil {
		return nil, err
	}
	return c.Client.CompleteMultipartUpload(ctx, in, o...)
}

func (c *counting) AbortMultipartUpload(ctx context.Context, in *s3.AbortMultipartUploadInput, o ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	if err := c.note("AbortMultipartUpload"); err != nil {
		return nil, err
	}
	return c.Client.AbortMultipartUpload(ctx, in, o...)
}

type fixture struct {
	raw    *s3.Client
	client *counting
	store  *teamblob.Store
}

func newFixture(t *testing.T, opts teamblob.Options) *fixture {
	t.Helper()
	srv := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(srv.Close)
	raw := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	if _, err := raw.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	c := &counting{Client: raw, calls: map[string]int{}, failOn: map[string]error{}}
	opts.Bucket = bucket
	opts.Client = c
	if opts.Prefix == "" {
		opts.Prefix = "svc"
	}
	st, err := teamblob.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{raw: raw, client: c, store: st}
}

func put(t *testing.T, st *teamblob.Store, team, rel, body string) {
	t.Helper()
	if _, err := st.Put(context.Background(), team, rel, strings.NewReader(body), teamblob.PutOptions{Size: int64(len(body))}); err != nil {
		t.Fatalf("put %s/%s: %v", team, rel, err)
	}
}

func TestTeamsCannotReachEachOthersObjects(t *testing.T) {
	t.Parallel()
	f := newFixture(t, teamblob.Options{})
	ctx := context.Background()
	put(t, f.store, "team-a", "cache/key.tar.gz", "secret-a")

	if _, _, err := f.store.Get(ctx, "team-b", "cache/key.tar.gz"); !errors.Is(err, teamblob.ErrNotFound) {
		t.Fatalf("team-b read team-a's key: err=%v", err)
	}
	if _, _, err := f.store.Get(ctx, "", "cache/key.tar.gz"); !errors.Is(err, teamblob.ErrNotFound) {
		t.Fatalf("the operator namespace read team-a's key: err=%v", err)
	}
	for _, rel := range []string{"../team-a/cache/key.tar.gz", "cache/../../team-a/x", "a//b", "./x", "x\\y", "", "/abs"} {
		if _, _, err := f.store.Get(ctx, "team-b", rel); !errors.Is(err, teamblob.ErrInvalidKey) {
			t.Errorf("rel %q: err=%v, want ErrInvalidKey", rel, err)
		}
	}
	for _, rel := range []string{"teams/team-a/cache/key.tar.gz", "_meta/usage.json"} {
		if _, _, err := f.store.Get(ctx, "", rel); !errors.Is(err, teamblob.ErrInvalidKey) {
			t.Errorf("operator rel %q: err=%v, want ErrInvalidKey", rel, err)
		}
	}
	for _, team := range []string{"Team-A", "../team-a", "team-a/x", "-a", "a-", strings.Repeat("a", 64)} {
		if _, _, err := f.store.Get(ctx, team, "cache/key.tar.gz"); !errors.Is(err, teamblob.ErrInvalidKey) {
			t.Errorf("team %q: err=%v, want ErrInvalidKey", team, err)
		}
	}
	if _, err := f.store.DeleteTeam(ctx, "team-b"); err != nil {
		t.Fatal(err)
	}
	body, err := f.store.ReadAll(ctx, "team-a", "cache/key.tar.gz")
	if err != nil || string(body) != "secret-a" {
		t.Fatalf("team-b's purge touched team-a: %q %v", body, err)
	}
	key, _ := f.store.Key("team-a", "cache/key.tar.gz")
	if key != "svc/teams/team-a/cache/key.tar.gz" {
		t.Fatalf("key = %q", key)
	}
}

// An overwrite reports the size difference and no new object, and a
// measurement lists each namespace once.
func TestPutReportsWhatItAddsAndMeasureListsEachNamespaceOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t, teamblob.Options{})
	ctx := context.Background()
	put(t, f.store, "team-a", "bins/one", "12345")
	put(t, f.store, "team-a", "bins/two", "123")
	w, err := f.store.Put(ctx, "team-a", "bins/one", strings.NewReader("12"), teamblob.PutOptions{Size: 2})
	if err != nil || w.Bytes != 2 || w.AddedBytes != -3 || w.AddedObjects != 0 {
		t.Fatalf("overwrite = %+v, %v; want 2 bytes stored, 3 fewer and no new object", w, err)
	}
	put(t, f.store, "team-b", "bins/one", "1234567")
	put(t, f.store, "", "bins/operator", "1")
	if err := f.store.Delete(ctx, "team-a", "bins/two"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Delete(ctx, "team-a", "bins/missing"); err != nil {
		t.Fatal(err)
	}

	f.client.reset()
	m, err := f.store.Measure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := f.client.count("ListObjectsV2"); n != 5 {
		t.Fatalf("measure listed %d times, want 5", n)
	}
	if m.Teams["team-a"] != (teamblob.Tally{Bytes: 2, Objects: 1}) || m.Teams["team-b"] != (teamblob.Tally{Bytes: 7, Objects: 1}) {
		t.Fatalf("teams = %+v", m.Teams)
	}
	if m.Operator != (teamblob.Tally{Bytes: 1, Objects: 1}) {
		t.Fatalf("operator = %+v", m.Operator)
	}

	d, err := f.store.DeleteTeam(ctx, "team-b")
	if err != nil || d.Objects != 1 || d.Bytes != 7 {
		t.Fatalf("delete team-b = %+v %v", d, err)
	}
	if m, err := f.store.Measure(ctx); err != nil || len(m.Teams) != 1 {
		t.Fatalf("after purge = %+v, %v; want only team-a", m.Teams, err)
	}
}

// A listing that fails leaves its namespace out and reports the error, so a
// caller never takes a partial measurement for the whole.
func TestMeasureReportsAFailedListing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, teamblob.Options{})
	put(t, f.store, "team-a", "bins/one", "12345")
	f.client.failOn["ListObjectsV2"] = errors.New("503 SlowDown")
	if _, err := f.store.Measure(context.Background()); err == nil {
		t.Fatal("a measurement whose listing failed reported success")
	}
}

func TestDeletePrefixCostsOneListAndOneDeletePerThousand(t *testing.T) {
	t.Parallel()
	f := newFixture(t, teamblob.Options{})
	ctx := context.Background()
	const n = 2500
	for i := range n {
		// perf: Fresh skips the per-object HEAD so seeding stays quick.
		if _, err := f.store.Put(ctx, "team-a", fmt.Sprintf("artifacts/job/%05d", i), strings.NewReader("x"), teamblob.PutOptions{Size: 1, Fresh: true}); err != nil {
			t.Fatal(err)
		}
	}
	put(t, f.store, "team-b", "artifacts/job/00000", "y")
	f.client.reset()
	d, err := f.store.DeletePrefix(ctx, "team-a", "artifacts/job/")
	if err != nil {
		t.Fatal(err)
	}
	if d.Objects != n || d.Bytes != n {
		t.Fatalf("deleted %+v, want %d objects", d, n)
	}
	if got := f.client.count("ListObjectsV2"); got != 3 {
		t.Errorf("LIST requests = %d, want 3 for %d objects", got, n)
	}
	if got := f.client.count("DeleteObjects"); got != 3 {
		t.Errorf("DeleteObjects requests = %d, want 3 for %d objects", got, n)
	}
	if got := f.client.count("DeleteObject") + f.client.count("HeadObject") + f.client.count("GetObject"); got != 0 {
		t.Errorf("a prefix delete spent %d per-object requests", got)
	}
	if body, err := f.store.ReadAll(ctx, "team-b", "artifacts/job/00000"); err != nil || string(body) != "y" {
		t.Fatalf("team-b's object went with team-a's prefix: %q %v", body, err)
	}
}

// failAfter yields n bytes and then an error, as a client that hangs up
// mid-upload does.
type failAfter struct {
	n   int
	err error
}

func (r *failAfter) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, r.err
	}
	k := min(len(p), r.n)
	for i := range p[:k] {
		p[i] = 'z'
	}
	r.n -= k
	return k, nil
}

func TestMultipartUploadRoundTripsAndAbortsOnFailure(t *testing.T) {
	t.Parallel()
	const part = 5 << 20
	f := newFixture(t, teamblob.Options{PartSize: part, MultipartThreshold: part})
	ctx := context.Background()

	body := bytes.Repeat([]byte("0123456789abcdef"), (2*part+part/2)/16)
	wr, err := f.store.Put(ctx, "team-a", "artifacts/big.bin", bytes.NewReader(body), teamblob.PutOptions{Size: -1})
	if err != nil {
		t.Fatal(err)
	}
	if wr.Bytes != int64(len(body)) || wr.AddedObjects != 1 {
		t.Fatalf("wrote %+v of %d bytes", wr, len(body))
	}
	if got := f.client.count("UploadPart"); got != 3 {
		t.Fatalf("parts = %d, want 3", got)
	}
	got, err := f.store.ReadAll(ctx, "team-a", "artifacts/big.bin")
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("round trip: %d bytes, err=%v", len(got), err)
	}

	f.client.reset()
	cut := errors.New("client hung up")
	if _, err := f.store.Put(ctx, "team-a", "artifacts/torn.bin", &failAfter{n: part + 10, err: cut}, teamblob.PutOptions{Size: -1}); !errors.Is(err, cut) {
		t.Fatalf("torn upload err = %v", err)
	}
	if got := f.client.count("AbortMultipartUpload"); got != 1 {
		t.Fatalf("aborts = %d, want 1", got)
	}
	if got := f.client.count("CompleteMultipartUpload"); got != 0 {
		t.Fatalf("a torn upload completed")
	}
	if _, err := f.store.Head(ctx, "team-a", "artifacts/torn.bin"); !errors.Is(err, teamblob.ErrNotFound) {
		t.Fatalf("a torn upload left an object: %v", err)
	}
	uploads, err := f.raw.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket)})
	if err != nil {
		t.Fatal(err)
	}
	if len(uploads.Uploads) != 0 {
		t.Fatalf("%d multipart uploads left open", len(uploads.Uploads))
	}

	f.client.reset()
	f.client.failOn["UploadPart"] = errors.New("503 SlowDown")
	if _, err := f.store.Put(ctx, "team-a", "artifacts/refused.bin", bytes.NewReader(body), teamblob.PutOptions{Size: -1}); err == nil {
		t.Fatal("a refused part did not fail the upload")
	}
	if got := f.client.count("UploadPart"); got != 1 {
		t.Fatalf("parts sent after the first refusal: %d, want 1", got)
	}
	if got := f.client.count("AbortMultipartUpload"); got != 1 {
		t.Fatalf("aborts = %d, want 1", got)
	}
}

// A bucket that answers every request with 503 costs at most the SDK's
// capped attempts per call: the store adds no retry loop of its own.
func TestFailingBucketCostsBoundedAttempts(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `<Error><Code>SlowDown</Code><Message>slow down</Message></Error>`)
	}))
	defer srv.Close()
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	t.Setenv("SPARKWING_S3_ENDPOINT", srv.URL)
	t.Setenv("SPARKWING_OBJECT_STORE_BREAKER", "off")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, b, prefix, err := storeurl.OpenS3(ctx, "s3://"+bucket+"/svc")
	if err != nil {
		t.Fatal(err)
	}
	st, err := teamblob.New(teamblob.Options{Bucket: b, Prefix: prefix, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Put(ctx, "team-a", "bins/x", strings.NewReader("abc"), teamblob.PutOptions{Size: 3, Fresh: true})
	if err == nil {
		t.Fatal("a put against a failing bucket succeeded")
	}
	if got := hits.Load(); got < 2 || got > storeurl.SDKMaxAttempts {
		t.Fatalf("one put cost %d requests, want 2..%d", got, storeurl.SDKMaxAttempts)
	}
}

// The bucket's kill switch answers PUT and LIST with 403. The store must
// stop sending both at once, keep reads and deletes working, and probe
// with one request only when the pause ends.
func TestAccessDeniedPausesWritesAndListingsWithoutRetrying(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	f := newFixture(t, teamblob.Options{Now: clock})
	ctx := context.Background()
	put(t, f.store, "team-a", "bins/kept", "kept")

	f.client.reset()
	f.client.failOn["PutObject"] = &smithy.GenericAPIError{Code: "AccessDenied", Message: "explicit deny"}
	f.client.failOn["ListObjectsV2"] = &smithy.GenericAPIError{Code: "AccessDenied", Message: "explicit deny"}
	if _, err := f.store.Put(ctx, "team-a", "bins/x", strings.NewReader("x"), teamblob.PutOptions{Size: 1, Fresh: true}); err == nil {
		t.Fatal("a denied put succeeded")
	}
	for range 20 {
		_, err := f.store.Put(ctx, "team-a", "bins/x", strings.NewReader("x"), teamblob.PutOptions{Size: 1, Fresh: true})
		if !errors.Is(err, teamblob.ErrSuspended) {
			t.Fatalf("put during the pause = %v, want ErrSuspended", err)
		}
	}
	if _, err := f.store.List(ctx, "team-a", "bins/"); !errors.Is(err, teamblob.ErrSuspended) {
		t.Fatalf("list during the pause = %v, want ErrSuspended", err)
	}
	if got := f.client.count("PutObject") + f.client.count("ListObjectsV2"); got != 1 {
		t.Fatalf("a denied bucket was sent %d PUT and LIST requests, want 1", got)
	}
	// A paused write spends nothing, not even the HEAD an overwrite check sends.
	f.client.reset()
	if _, err := f.store.Put(ctx, "team-a", "bins/x", strings.NewReader("x"), teamblob.PutOptions{Size: 1}); !errors.Is(err, teamblob.ErrSuspended) {
		t.Fatalf("put during the pause = %v", err)
	}
	if got := f.client.total(); got != 0 {
		t.Fatalf("a paused write sent %d requests", got)
	}
	if st := f.store.Breaker(); !st.Open || st.Until.Sub(clock()) != time.Minute {
		t.Fatalf("breaker = %+v", st)
	}
	if body, err := f.store.ReadAll(ctx, "team-a", "bins/kept"); err != nil || string(body) != "kept" {
		t.Fatalf("a read during the pause = %q %v", body, err)
	}
	if err := f.store.Delete(ctx, "team-a", "bins/kept"); err != nil {
		t.Fatalf("a delete during the pause = %v", err)
	}

	// The pause ends: one probe, still denied, pauses twice as long.
	advance(time.Minute)
	f.client.reset()
	for range 5 {
		_, _ = f.store.Put(ctx, "team-a", "bins/x", strings.NewReader("x"), teamblob.PutOptions{Size: 1, Fresh: true})
	}
	if got := f.client.count("PutObject"); got != 1 {
		t.Fatalf("the end of the pause let %d PUTs through, want 1 probe", got)
	}
	if st := f.store.Breaker(); st.Until.Sub(clock()) != 2*time.Minute {
		t.Fatalf("second pause = %s", st.Until.Sub(clock()))
	}
	// The pause is capped at five minutes however long the deny lasts.
	for range 6 {
		advance(10 * time.Minute)
		_, _ = f.store.Put(ctx, "team-a", "bins/x", strings.NewReader("x"), teamblob.PutOptions{Size: 1, Fresh: true})
	}
	if st := f.store.Breaker(); st.Until.Sub(clock()) != 5*time.Minute {
		t.Fatalf("capped pause = %s", st.Until.Sub(clock()))
	}

	// The deny lifts: the next probe succeeds and closes the breaker.
	delete(f.client.failOn, "PutObject")
	delete(f.client.failOn, "ListObjectsV2")
	advance(5 * time.Minute)
	put(t, f.store, "team-a", "bins/y", "y")
	if st := f.store.Breaker(); st.Open {
		t.Fatalf("breaker stayed open after a successful probe: %+v", st)
	}
	put(t, f.store, "team-a", "bins/z", "z")
}

// Failures other than a deny pause writes after a few in a row, so an
// outage costs a bounded number of requests rather than one per caller.
func TestRepeatedFailuresPauseWrites(t *testing.T) {
	t.Parallel()
	f := newFixture(t, teamblob.Options{})
	ctx := context.Background()
	f.client.failOn["PutObject"] = &smithy.GenericAPIError{Code: "SlowDown", Message: "slow down"}
	for range 50 {
		_, _ = f.store.Put(ctx, "team-a", "bins/x", strings.NewReader("x"), teamblob.PutOptions{Size: 1, Fresh: true})
	}
	if got := f.client.count("PutObject"); got != 3 {
		t.Fatalf("50 writes against a failing bucket sent %d PUTs, want 3", got)
	}
}

// A prefix delete reports only deletions the store confirmed: a key a
// batch delete reports as failed, or a listing that fails, is not reported
// as gone.
func TestPrefixDeleteCountsOnlyConfirmedDeletions(t *testing.T) {
	t.Parallel()
	f := newFixture(t, teamblob.Options{})
	ctx := context.Background()
	for i := range 10 {
		put(t, f.store, "team-a", fmt.Sprintf("artifacts/job/%02d", i), "12345")
	}
	f.client.keepKey = func(key string) bool { return strings.HasSuffix(key, "/03") || strings.HasSuffix(key, "/07") }
	d, err := f.store.DeletePrefix(ctx, "team-a", "artifacts/job/")
	if err == nil {
		t.Fatal("a delete with refused keys reported success")
	}
	if d.Objects != 8 || d.Bytes != 40 {
		t.Fatalf("a partly refused delete reported %+v, want the 8 objects it removed", d)
	}
	objs, err := f.store.List(ctx, "team-a", "artifacts/job/")
	if err != nil || len(objs) != 2 {
		t.Fatalf("stored after the delete: %d objects, %v", len(objs), err)
	}

	f.client.keepKey = nil
	f.client.failOn["ListObjectsV2"] = errors.New("503 SlowDown")
	d, err = f.store.DeletePrefix(ctx, "team-a", "artifacts/job/")
	if err == nil || d.Objects != 0 {
		t.Fatalf("a delete whose listing failed = %+v, %v; want nothing removed and an error", d, err)
	}
	delete(f.client.failOn, "ListObjectsV2")
	f.client.failOn["DeleteObjects"] = errors.New("503 SlowDown")
	if err := f.store.DeleteMany(ctx, "team-a", []teamblob.Sized{{Rel: "artifacts/job/03", Size: 5}}); err == nil {
		t.Fatal("a refused batch delete reported success")
	}
}

// A store with a maximum age deletes a team's old objects in the
// measurement's own listing, and leaves them out of the tally; the
// operator's objects stay.
func TestMeasureExpiresOldTeamObjects(t *testing.T) {
	ctx := context.Background()
	clock := time.Now()
	f := newFixture(t, teamblob.Options{
		TeamObjectMaxAge: func(team string) time.Duration {
			if team == "keeper" {
				return 0
			}
			return time.Hour
		},
		Now: func() time.Time { return clock },
	})
	put(t, f.store, "team-a", "artifacts/run-1/old.txt", "old!")
	put(t, f.store, "keeper", "artifacts/run-1/old.txt", "kept")
	put(t, f.store, "", "bins/operator", "kept")

	m, err := f.store.Measure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Teams["team-a"]; got.Bytes != 4 || got.Objects != 1 {
		t.Fatalf("a fresh object = %+v, want it kept", got)
	}
	clock = clock.Add(2 * time.Hour)
	f.client.reset()
	if m, err = f.store.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.Teams["team-a"]; got.Bytes != 0 || got.Objects != 0 {
		t.Fatalf("team-a after its object aged out = %+v, want nothing", got)
	}
	if m.Expired != (teamblob.Tally{Bytes: 4, Objects: 1}) {
		t.Fatalf("expired = %+v, want team-a's one object", m.Expired)
	}
	if objs, err := f.store.List(ctx, "team-a", "artifacts/"); err != nil || len(objs) != 0 {
		t.Fatalf("team-a still lists %v, %v", objs, err)
	}
	if _, err := f.store.Head(ctx, "", "bins/operator"); err != nil {
		t.Fatalf("the operator's object was expired: %v", err)
	}
	if got := m.Teams["keeper"]; got.Objects != 1 {
		t.Fatalf("a team with no maximum age = %+v, want its object kept", got)
	}
	if n := f.client.count("DeleteObjects"); n != 1 {
		t.Fatalf("expiry sent %d batch deletes, want one", n)
	}
}
