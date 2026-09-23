package logs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
)

const archiveBucket = "logs-archive"

// billed counts object-store requests by operation and can refuse one.
type billed struct {
	teamblob.Client
	mu     sync.Mutex
	calls  map[string]int
	refuse map[string]bool
}

func (b *billed) note(op string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls[op]++
	if b.refuse[op] {
		return errors.New("503 SlowDown")
	}
	return nil
}

func (b *billed) count(op string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[op]
}

func (b *billed) total() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, c := range b.calls {
		n += c
	}
	return n
}

func (b *billed) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = map[string]int{}
}

func (b *billed) GetObject(ctx context.Context, in *s3.GetObjectInput, o ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if err := b.note("GetObject"); err != nil {
		return nil, err
	}
	return b.Client.GetObject(ctx, in, o...)
}

func (b *billed) PutObject(ctx context.Context, in *s3.PutObjectInput, o ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if err := b.note("PutObject"); err != nil {
		return nil, err
	}
	return b.Client.PutObject(ctx, in, o...)
}

func (b *billed) HeadObject(ctx context.Context, in *s3.HeadObjectInput, o ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if err := b.note("HeadObject"); err != nil {
		return nil, err
	}
	return b.Client.HeadObject(ctx, in, o...)
}

func (b *billed) DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, o ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if err := b.note("DeleteObject"); err != nil {
		return nil, err
	}
	return b.Client.DeleteObject(ctx, in, o...)
}

func (b *billed) DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, o ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	if err := b.note("DeleteObjects"); err != nil {
		return nil, err
	}
	return b.Client.DeleteObjects(ctx, in, o...)
}

func (b *billed) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, o ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if err := b.note("ListObjectsV2"); err != nil {
		return nil, err
	}
	return b.Client.ListObjectsV2(ctx, in, o...)
}

// archiveFixture is a logs service with an archive in an in-memory
// bucket, in front of a fake controller that answers yes to every run
// question, so the tests see what the logs service enforces on its own.
type archiveFixture struct {
	srv    *Server
	http   *httptest.Server
	raw    *s3.Client
	client *billed
	root   string
}

func newArchiveFixture(t *testing.T, retention time.Duration) *archiveFixture {
	t.Helper()
	fake := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(fake.Close)
	raw := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(fake.URL),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	if _, err := raw.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(archiveBucket)}); err != nil {
		t.Fatal(err)
	}
	client := &billed{Client: raw, calls: map[string]int{}, refuse: map[string]bool{}}
	store, err := teamblob.New(teamblob.Options{Bucket: archiveBucket, Prefix: "logs", Client: client})
	if err != nil {
		t.Fatal(err)
	}

	principals := map[string]whoamiResp{
		"Bearer a":     {Principal: "a", Kind: "user", Scopes: []string{scopeLogsRead, scopeLogsWrite}, Team: "team-a"},
		"Bearer b":     {Principal: "b", Kind: "user", Scopes: []string{scopeLogsRead, scopeLogsWrite}, Team: "team-b"},
		"Bearer admin": {Principal: "admin", Kind: "user", Scopes: []string{scopeAdmin}, Team: "default"},
	}
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principals[r.Header.Get("Authorization")]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/v1/auth/whoami" {
			_ = json.NewEncoder(w).Encode(p)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(ctrl.Close)

	root := t.TempDir()
	srv, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.Retention = retention
	srv.WithLimits(limits)
	srv.WithControllerAuth(ctrl.URL, time.Minute)
	srv.WithArchive(ArchiveOptions{Store: store})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return &archiveFixture{srv: srv, http: hs, raw: raw, client: client, root: root}
}

func (f *archiveFixture) do(t *testing.T, method, path, bearer, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.http.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", bearer)
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (f *archiveFixture) keys(t *testing.T) map[string]bool {
	t.Helper()
	out, err := f.raw.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String(archiveBucket)})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, o := range out.Contents {
		keys[aws.ToString(o.Key)] = true
	}
	return keys
}

func (f *archiveFixture) onVolume(runID string) bool {
	_, err := os.Stat(filepath.Join(f.root, "runs", runID))
	return err == nil
}

// age backdates every file of a run so the archiver and retention see it
// as last written at when.
func (f *archiveFixture) age(t *testing.T, runID string, when time.Time) {
	t.Helper()
	err := filepath.Walk(filepath.Join(f.root, "runs", runID), func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, when, when)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArchiveMovesIdleRunsToTheirTeamsNamespaceAndRestoresThem(t *testing.T) {
	f := newArchiveFixture(t, 0)
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", "line one\n"); code != http.StatusNoContent {
		t.Fatalf("append = %d %s", code, body)
	}
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a/test", "Bearer a", "tests pass\n"); code != http.StatusNoContent {
		t.Fatalf("append = %d %s", code, body)
	}

	f.client.reset()
	n, err := f.srv.ArchiveOnce(context.Background(), time.Now())
	if err != nil || n != 0 {
		t.Fatalf("a live run was archived: n=%d err=%v", n, err)
	}
	if f.client.total() != 0 {
		t.Fatalf("looking for idle runs cost %d requests", f.client.total())
	}
	n, err = f.srv.ArchiveOnce(context.Background(), time.Now().Add(DefaultArchiveIdle+time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("archive = %d, %v", n, err)
	}
	// Two log files, the run index and the day entry: four PUTs, no LIST.
	if got := f.client.count("PutObject"); got != 4 {
		t.Errorf("archiving a two-file run cost %d PUTs, want 4", got)
	}
	if got := f.client.count("ListObjectsV2"); got != 0 {
		t.Errorf("archiving listed the store %d times", got)
	}
	if f.onVolume("run-a") {
		t.Fatal("an archived run stayed on the volume")
	}
	keys := f.keys(t)
	day := time.Now().UTC().Format(dayLayout)
	for _, k := range []string{
		"logs/teams/team-a/runs/run-a/build.log",
		"logs/teams/team-a/runs/run-a/test.log",
		"logs/index/runs/run-a.json",
		"logs/index/days/" + day + "/run-a",
	} {
		if !keys[k] {
			t.Errorf("missing %s in %v", k, keys)
		}
	}

	f.client.reset()
	code, body := f.do(t, http.MethodGet, "/api/v1/logs/run-a/build", "Bearer a", "")
	if code != http.StatusOK || body != "line one\n" {
		t.Fatalf("read of an archived run = %d %q", code, body)
	}
	// One index read and one read per file; no listing.
	if got, lists := f.client.count("GetObject"), f.client.count("ListObjectsV2"); got != 3 || lists != 0 {
		t.Errorf("restoring cost %d GETs and %d LISTs, want 3 and 0", got, lists)
	}
	f.client.reset()
	if code, body := f.do(t, http.MethodGet, "/api/v1/logs/run-a", "Bearer a", ""); code != http.StatusOK || !strings.Contains(body, "tests pass") {
		t.Fatalf("whole-run read = %d %q", code, body)
	}
	if f.client.total() != 0 {
		t.Errorf("a second read of a restored run cost %d requests", f.client.total())
	}

	// Written again after archiving, the run keeps both appends.
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", "line two\n"); code != http.StatusNoContent {
		t.Fatalf("append after archive = %d %s", code, body)
	}
	if _, err := f.srv.ArchiveOnce(context.Background(), time.Now().Add(DefaultArchiveIdle+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if f.onVolume("run-a") {
		t.Fatal("the rewritten run stayed on the volume")
	}
	if code, body := f.do(t, http.MethodGet, "/api/v1/logs/run-a/build", "Bearer a", ""); body != "line one\nline two\n" {
		t.Fatalf("read after a second archive = %d %q", code, body)
	}

	// A run nobody ever wrote costs one index read, then none.
	f.client.reset()
	for range 3 {
		if code, body := f.do(t, http.MethodGet, "/api/v1/logs/run-new/build", "Bearer a", ""); code != http.StatusOK || body != "" {
			t.Fatalf("read of an unknown run = %d %q", code, body)
		}
	}
	if got := f.client.count("GetObject"); got != 1 {
		t.Errorf("three reads of an unknown run cost %d GETs, want 1", got)
	}
}

// The fake controller says yes to every run question, so these refusals
// are the logs service's own check against the run's recorded team.
func TestArchivedRunsStayInsideTheirTeam(t *testing.T) {
	f := newArchiveFixture(t, 0)
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", "team-a secret\n"); code != http.StatusNoContent {
		t.Fatalf("append = %d %s", code, body)
	}
	check := func(stage string) {
		t.Helper()
		for _, path := range []string{"/api/v1/logs/run-a/build", "/api/v1/logs/run-a", "/api/v1/logs/search?q=secret&run_id=run-a"} {
			if code, body := f.do(t, http.MethodGet, path, "Bearer b", ""); code != http.StatusNotFound || strings.Contains(body, "secret") {
				t.Errorf("%s: team B read %s: %d %q", stage, path, code, body)
			}
		}
		if code, _ := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer b", "poison\n"); code/100 == 2 {
			t.Errorf("%s: team B appended to team A's run: %d", stage, code)
		}
		if code, _ := f.do(t, http.MethodDelete, "/api/v1/logs/run-a", "Bearer b", ""); code/100 == 2 {
			t.Errorf("%s: team B deleted team A's run: %d", stage, code)
		}
		for _, path := range []string{"/api/v1/teams/team-a/logs", "/api/v1/teams/team-a/logs/usage"} {
			method := http.MethodDelete
			if strings.HasSuffix(path, "usage") {
				method = http.MethodGet
			}
			if code, _ := f.do(t, method, path, "Bearer a", ""); code != http.StatusForbidden {
				t.Errorf("%s: %s %s without admin = %d, want 403", stage, method, path, code)
			}
		}
	}
	check("on the volume")
	if _, err := f.srv.ArchiveOnce(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if f.onVolume("run-a") {
		t.Fatal("run stayed on the volume")
	}
	check("archived")
	if code, body := f.do(t, http.MethodGet, "/api/v1/logs/run-a/build", "Bearer a", ""); body != "team-a secret\n" {
		t.Fatalf("team A's own read = %d %q", code, body)
	}
}

func TestTeamPurgeDeletesOnlyThatTeamsLogs(t *testing.T) {
	f := newArchiveFixture(t, 0)
	for _, w := range []struct{ run, bearer string }{{"run-a1", "Bearer a"}, {"run-a2", "Bearer a"}, {"run-b1", "Bearer b"}} {
		if code, body := f.do(t, http.MethodPost, "/api/v1/logs/"+w.run+"/build", w.bearer, w.run+"\n"); code != http.StatusNoContent {
			t.Fatalf("append = %d %s", code, body)
		}
	}
	f.age(t, "run-a1", time.Now().Add(-time.Hour))
	f.age(t, "run-b1", time.Now().Add(-time.Hour))
	if _, err := f.srv.ArchiveOnce(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if f.onVolume("run-a1") || !f.onVolume("run-a2") {
		t.Fatal("archiver did not take exactly the idle runs")
	}

	code, body := f.do(t, http.MethodGet, "/api/v1/teams/team-a/logs/usage", "Bearer admin", "")
	var u TeamLogsUsage
	if err := json.Unmarshal([]byte(body), &u); code != http.StatusOK || err != nil || u.Objects != 1 || u.Bytes != int64(len("run-a1\n")) {
		t.Fatalf("usage = %d %s", code, body)
	}

	code, body = f.do(t, http.MethodDelete, "/api/v1/teams/team-a/logs", "Bearer admin", "")
	if code != http.StatusOK {
		t.Fatalf("purge = %d %s", code, body)
	}
	if f.onVolume("run-a2") {
		t.Error("the purge left team A's live run on the volume")
	}
	for k := range f.keys(t) {
		if strings.HasPrefix(k, "logs/teams/team-a/") {
			t.Errorf("the purge left %s", k)
		}
	}
	if code, body := f.do(t, http.MethodGet, "/api/v1/logs/run-b1/build", "Bearer b", ""); body != "run-b1\n" {
		t.Errorf("team B's run went with team A's purge: %d %q", code, body)
	}
	if code, body := f.do(t, http.MethodGet, "/api/v1/logs/run-a1/build", "Bearer a", ""); body != "" {
		t.Errorf("a purged run still reads: %d %q", code, body)
	}
	if code, _ := f.do(t, http.MethodDelete, "/api/v1/teams/team-a/logs", "Bearer admin", ""); code != http.StatusOK {
		t.Errorf("a repeated purge = %d, want it to succeed", code)
	}
	if code, _ := f.do(t, http.MethodDelete, "/api/v1/teams/Team..A/logs", "Bearer admin", ""); code != http.StatusBadRequest {
		t.Errorf("a purge of a non-slug = %d, want 400", code)
	}
}

// Retention lists the day index and one day's entries, reads each expired
// run's index once, and deletes in batches: a fixed handful of requests
// for any number of runs under a thousand files, never a listing of the
// runs' objects.
func TestRetentionOnTheArchiveCostsBoundedRequests(t *testing.T) {
	const retention = 7 * 24 * time.Hour
	f := newArchiveFixture(t, retention)
	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour)
	const expired = 40
	for i := range expired {
		run := fmt.Sprintf("old-%02d", i)
		bearer := "Bearer a"
		if i%2 == 1 {
			bearer = "Bearer b"
		}
		for _, node := range []string{"build", "test"} {
			if code, body := f.do(t, http.MethodPost, "/api/v1/logs/"+run+"/"+node, bearer, "x\n"); code != http.StatusNoContent {
				t.Fatalf("append = %d %s", code, body)
			}
		}
		f.age(t, run, old)
	}
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/recent/build", "Bearer a", "keep me\n"); code != http.StatusNoContent {
		t.Fatalf("append = %d %s", code, body)
	}
	f.age(t, "recent", now.Add(-2*24*time.Hour))
	if n, err := f.srv.ArchiveOnce(context.Background(), now); err != nil || n != expired+1 {
		t.Fatalf("archive = %d, %v", n, err)
	}

	f.client.reset()
	pruned, err := f.srv.PruneArchive(context.Background(), now)
	if err != nil || pruned != expired {
		t.Fatalf("prune = %d, %v", pruned, err)
	}
	if got := f.client.count("ListObjectsV2"); got != 2 {
		t.Errorf("prune listed %d times, want 2 (the days, then the expired day)", got)
	}
	if got := f.client.count("GetObject"); got != expired {
		t.Errorf("prune read %d indexes, want %d", got, expired)
	}
	// One batch per team namespace and one for the operator's index keys.
	if got := f.client.count("DeleteObjects"); got != 3 {
		t.Errorf("prune sent %d delete batches, want 3", got)
	}
	if got := f.client.count("DeleteObject") + f.client.count("HeadObject") + f.client.count("PutObject"); got != 0 {
		t.Errorf("prune spent %d per-object requests", got)
	}

	keys := f.keys(t)
	for k := range keys {
		if strings.Contains(k, "old-") {
			t.Errorf("expired %s survived", k)
		}
	}
	if !keys["logs/teams/team-a/runs/recent/build.log"] {
		t.Error("a run inside the retention was pruned")
	}

	// With nothing expired, a pass is one listing.
	f.client.reset()
	if _, err := f.srv.PruneArchive(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if got := f.client.total(); got != 1 {
		t.Errorf("an idle prune cost %d requests, want 1", got)
	}
}

// A refusing store costs one request per pass, not one per idle run, and
// the pass after a failure waits out its backoff without sending any.
func TestArchiveStopsAtTheFirstFailureAndBacksOff(t *testing.T) {
	f := newArchiveFixture(t, 0)
	for i := range 5 {
		run := fmt.Sprintf("run-%d", i)
		if code, body := f.do(t, http.MethodPost, "/api/v1/logs/"+run+"/build", "Bearer a", "x\n"); code != http.StatusNoContent {
			t.Fatalf("append = %d %s", code, body)
		}
	}
	f.client.reset()
	f.client.refuse["PutObject"] = true
	now := time.Now().Add(time.Hour)
	if _, err := f.srv.ArchiveOnce(context.Background(), now); err == nil {
		t.Fatal("archiving into a refusing store succeeded")
	}
	if got := f.client.count("PutObject"); got != 1 {
		t.Fatalf("a refusing store was sent %d PUTs in one pass, want 1", got)
	}
	f.client.reset()
	if _, err := f.srv.ArchiveOnce(context.Background(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := f.client.total(); got != 0 {
		t.Fatalf("the pass inside the backoff sent %d requests", got)
	}
	for i := range 5 {
		if !f.onVolume(fmt.Sprintf("run-%d", i)) {
			t.Fatalf("run-%d left the volume without reaching the store", i)
		}
	}
	f.client.refuse["PutObject"] = false
	if n, err := f.srv.ArchiveOnce(context.Background(), now.Add(minArchiveBackoff+maxArchiveBackoff+time.Second)); err != nil || n != 5 {
		t.Fatalf("after the backoff: %d, %v", n, err)
	}
}

// A chatty node, a thousand lines a second for a minute, costs the object
// store nothing while it runs: appends land on the volume. Once the run
// goes idle it costs one PUT per node log plus two index PUTs for the run.
func TestAChattyNodeCostsNoObjectStoreWritesWhileItRuns(t *testing.T) {
	f := newArchiveFixture(t, 0)
	line := strings.Repeat("x", 80) + "\n"
	second := strings.Repeat(line, 1000)
	f.client.reset()
	for range 60 {
		if code, body := f.do(t, http.MethodPost, "/api/v1/logs/chatty/build", "Bearer a", second); code != http.StatusNoContent {
			t.Fatalf("append = %d %s", code, body)
		}
	}
	// The run's first request asks the index whether the run was archived
	// before; nothing else reaches the store.
	if puts, gets := f.client.count("PutObject"), f.client.count("GetObject"); puts != 0 || gets != 1 || f.client.total() != 1 {
		t.Fatalf("a minute of appends cost %d PUTs, %d GETs, %d requests in all; want 0, 1, 1", puts, gets, f.client.total())
	}
	if _, err := f.srv.ArchiveOnce(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if puts := f.client.count("PutObject"); puts != 3 {
		t.Fatalf("archiving the chatty node cost %d PUTs, want 3", puts)
	}
	t.Logf("chatty node: 60000 lines over 60 appends; %d PUTs for the whole run", f.client.count("PutObject"))
	if code, body := f.do(t, http.MethodGet, "/api/v1/logs/chatty/build?tail=1", "Bearer a", ""); code != http.StatusOK || body != line {
		t.Fatalf("tail of the archived node = %d %q", code, body)
	}
}

// Following a running node streams from the volume: lines appear within
// the stream's poll interval and the object store is never asked.
func TestFollowStreamsLiveLinesWithoutTouchingTheObjectStore(t *testing.T) {
	f := newArchiveFixture(t, 0)
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/live/build", "Bearer a", "first\n"); code != http.StatusNoContent {
		t.Fatalf("append = %d %s", code, body)
	}
	f.client.reset()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.http.URL+"/api/v1/logs/live/build/stream", nil)
	req.Header.Set("Authorization", "Bearer a")
	resp, err := f.http.Client().Do(req) //nolint:bodyclose // closed by the defer below; a goroutine reads it
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d", resp.StatusCode)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		buf := make([]byte, 4096)
		var pending string
		for {
			n, err := resp.Body.Read(buf)
			pending += string(buf[:n])
			for {
				i := strings.Index(pending, "\n")
				if i < 0 {
					break
				}
				if l := pending[:i]; strings.HasPrefix(l, "data: ") {
					lines <- strings.TrimPrefix(l, "data: ")
				}
				pending = pending[i+1:]
			}
			if err != nil {
				return
			}
		}
	}()
	want := func(text string) {
		t.Helper()
		deadline := time.After(time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream ended before %q", text)
				}
				if l == text {
					return
				}
			case <-deadline:
				t.Fatalf("%q did not arrive within a second", text)
			}
		}
	}
	want("first")
	for i := range 3 {
		text := fmt.Sprintf("live line %d", i)
		start := time.Now()
		if code, body := f.do(t, http.MethodPost, "/api/v1/logs/live/build", "Bearer a", text+"\n"); code != http.StatusNoContent {
			t.Fatalf("append = %d %s", code, body)
		}
		want(text)
		t.Logf("%q reached the follower in %s", text, time.Since(start).Round(time.Millisecond))
	}
	// The run is held by its follower, so the archiver leaves it alone.
	if n, err := f.srv.ArchiveOnce(context.Background(), time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("the archiver took a followed run: %d, %v", n, err)
	}
	if got := f.client.total(); got != 0 {
		t.Fatalf("following a live node cost %d object-store requests", got)
	}
}
