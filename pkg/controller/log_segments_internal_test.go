package controller

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type logSegmentFixture struct {
	dispatchRouteFixture
	srv *Server
	url string
}

func newLogSegmentFixture(t *testing.T) logSegmentFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now()
	if err := st.CreateRun(t.Context(), store.Run{ID: "run-d", Pipeline: "demo", Status: "pending", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePlanNode(t.Context(), store.DefaultTeam, "run-d", now); err != nil {
		t.Fatal(err)
	}
	srv := New(st, nil).EnableAuthFromStore()
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	f := logSegmentFixture{dispatchRouteFixture: dispatchRouteFixture{st: st, handler: srv.Handler()}, srv: srv, url: hs.URL}
	plan := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	wantPost(t, f.dispatchRouteFixture, planPath, plan, `{"nodes":[{"id":"a","deps":[],`+dispatchHash+`},{"id":"b","deps":[],`+dispatchHash+`}]}`,
		http.StatusOK, "accepted")
	return f
}

type signalUploader struct {
	*client.LogSegmentUploader
	stored chan int
}

func (u signalUploader) PutSegment(ctx context.Context, seg logseg.Segment, body []byte) error {
	err := u.LogSegmentUploader.PutSegment(ctx, seg, body)
	if err == nil {
		u.stored <- seg.N
	}
	return err
}

func (f logSegmentFixture) sink(t *testing.T, token, nodeID string) (*logseg.Sink, signalUploader) {
	t.Helper()
	up := signalUploader{
		LogSegmentUploader: client.NewWithToken(f.url, nil, token).NewLogSegmentUploader(t.Context(), "run-d", nodeID),
		stored:             make(chan int, 64),
	}
	if err := up.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := logseg.NewSink(t.Context(), up, logseg.Options{
		Run: "run-d", Node: nodeID, SpoolDir: t.TempDir(), SegmentBytes: 16,
		RetryBase: time.Millisecond, RetryMax: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, up
}

func logLine(i int) string { return fmt.Sprintf("line %02d\n", i) }

func TestLogSegmentsRoundTripThroughTheController(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	tok := f.claim(t, "a", store.ClaimTokenWork)
	s, _ := f.sink(t, tok, "a")
	var want strings.Builder
	for i := 1; i <= 6; i++ {
		if err := s.Append([]byte(logLine(i)), "", 0, 0); err != nil {
			t.Fatal(err)
		}
		want.WriteString(logLine(i))
	}
	m, err := s.Close(ctx, logseg.End{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != logseg.StatusComplete || len(m.Segments) < 2 {
		t.Fatalf("manifest = %+v", m)
	}
	logs := f.srv.dashboardLogs(store.DefaultTeam)
	got, err := logs.Read(ctx, "run-d", "a", storage.ReadOpts{})
	if err != nil || string(got) != want.String() {
		t.Fatalf("read %q, %v; want %q", got, err, want.String())
	}
	if tail, _ := logs.Read(ctx, "run-d", "a", storage.ReadOpts{Tail: 1}); string(tail) != logLine(6) {
		t.Fatalf("tail read %q", tail)
	}
	if other, err := f.srv.dashboardLogs("other").Read(ctx, "run-d", "a", storage.ReadOpts{}); err != nil || len(other) != 0 {
		t.Fatalf("another team read the log: %q, %v", other, err)
	}
	c, found, err := logs.(segmentLogs).NodeLogCompleteness(ctx, "run-d", "a",
		logseg.NodeProgress{Started: true, Terminal: true, FinishedAt: time.Now()}, time.Now())
	if err != nil || !found || c.State != logseg.StateComplete || c.Lines != 6 {
		t.Fatalf("completeness = %+v %v %v", c, found, err)
	}

	rec, err := f.st.LatestNodeLog(ctx, store.DefaultTeam, "run-d", "a")
	if err != nil || !strings.HasPrefix(rec.Prefix, "logs/r90/teams/default/runs/run-d/a/a1-g") {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	stored, err := logseg.Open(ctx, f.srv.logObjects(), rec.Prefix)
	if err != nil || stored.Manifest == nil || stored.Manifest.Attempt != 1 || stored.Manifest.Generation < 1 {
		t.Fatalf("stored manifest = %+v, %v", stored, err)
	}
}

func TestLogSegmentsRefuseASupersededAttemptAndASecondManifest(t *testing.T) {
	f := newLogSegmentFixture(t)
	tok := f.claim(t, "a", store.ClaimTokenWork)
	code, body := f.do(t, http.MethodPost, "/api/v1/runs/run-d/nodes/a/log-segments", tok, []byte(`{"attempt":7,"generation":1,"next":1,"count":1}`))
	if code != http.StatusConflict {
		t.Fatalf("a superseded attempt asked for URLs: %d %s", code, body)
	}
	code, body = f.do(t, http.MethodPost, "/api/v1/runs/run-d/nodes/b/log-segments", tok, []byte(`{"next":1,"count":1}`))
	if code == http.StatusOK {
		t.Fatalf("a's claim took URLs for node b: %d %s", code, body)
	}
	s, up := f.sink(t, tok, "a")
	if _, err := s.Close(t.Context(), logseg.End{}); err != nil {
		t.Fatal(err)
	}
	if err := up.PutManifest(t.Context(), logseg.Manifest{Version: logseg.ManifestVersion, Status: logseg.StatusComplete}); !errors.Is(err, logseg.ErrSealed) {
		t.Fatalf("a second manifest = %v, want ErrSealed", err)
	}
}

// A runner that dies mid-node never seals its attempt. Once the node is
// finished and the grace has passed, the controller seals what landed
// and the log reads incomplete.
func TestControllerSealsAnAttemptItsRunnerAbandoned(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	tok := f.claim(t, "b", store.ClaimTokenWork)
	s, up := f.sink(t, tok, "b")
	for i := 1; i <= 5; i++ {
		if err := s.Append([]byte(logLine(i)), "", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	for n := range up.stored {
		if n >= 2 {
			break
		}
	}
	wantPost(t, f.dispatchRouteFixture, "/api/v1/runs/run-d/nodes/b/attempt", tok, `{"outcome":"failed","error":"runner lost"}`, http.StatusOK, "")

	f.srv.sealAbandonedLogs(ctx, time.Now().Add(2*logseg.SealGrace))
	rec, err := f.st.LatestNodeLog(ctx, store.DefaultTeam, "run-d", "b")
	if err != nil || rec.SealedAt.IsZero() {
		t.Fatalf("the abandoned attempt was not sealed: %+v, %v", rec, err)
	}
	logs := f.srv.dashboardLogs(store.DefaultTeam).(segmentLogs)
	got, err := logs.Read(ctx, "run-d", "b", storage.ReadOpts{})
	if err != nil || !strings.HasPrefix("line 01\nline 02\nline 03\nline 04\nline 05\n", string(got)) || len(got) == 0 {
		t.Fatalf("read %q, %v", got, err)
	}
	c, _, err := logs.NodeLogCompleteness(ctx, "run-d", "b", logseg.NodeProgress{Started: true, Terminal: true}, time.Now())
	if err != nil || c.State != logseg.StateIncomplete || !strings.Contains(c.Message, logseg.ReasonRunnerLost) {
		t.Fatalf("completeness = %+v, %v", c, err)
	}
	if _, err := s.Close(context.Background(), logseg.End{}); err == nil {
		t.Fatal("the lost runner sealed after the controller did")
	}

	admin := f.admin(t)
	if code, raw := f.do(t, http.MethodDelete, "/api/v1/runs/run-d", admin, nil); code != http.StatusNoContent {
		t.Fatalf("delete run: %d %s", code, raw)
	}
	if left, _ := f.srv.logObjects().List(ctx, rec.Prefix); len(left) != 0 {
		t.Fatalf("deleting the run left its log objects: %v", left)
	}
}

// A viewer of a running segment node sees the segments uploaded so far
// and then the live ring from the byte where they end, each line once,
// until the manifest ends the stream.
func TestSegmentStreamJoinsTheLiveRingByOffset(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	tok := f.claim(t, "a", store.ClaimTokenWork)
	up := client.NewWithToken(f.url, nil, tok).NewLogSegmentUploader(ctx, "run-d", "a")
	if err := up.Open(ctx); err != nil {
		t.Fatal(err)
	}
	stored := make(chan int, 64)
	s, err := logseg.NewSink(ctx, signalUploader{LogSegmentUploader: up, stored: stored}, logseg.Options{
		Run: "run-d", Node: "a", SpoolDir: t.TempDir(), SegmentBytes: 16,
		Live: func(off int64, data []byte) { f.srv.liveLogs.AppendAt("run-d", "a", off, data) },
	})
	if err != nil {
		t.Fatal(err)
	}
	s.SetWatched(true)
	var want []string
	for i := 1; i <= 5; i++ {
		if err := s.Append([]byte(logLine(i)), "", 0, 0); err != nil {
			t.Fatal(err)
		}
		want = append(want, strings.TrimSuffix(logLine(i), "\n"))
	}
	for n := range stored {
		if n >= 2 {
			break
		}
	}
	body, err := f.srv.dashboardLogs(store.DefaultTeam).Stream(ctx, "run-d", "a")
	if err != nil || body == nil {
		t.Fatalf("stream = %v, %v", body, err)
	}
	defer func() { _ = body.Close() }()
	lines := bufio.NewScanner(body)
	var got []string
	next := func() (string, bool) {
		for lines.Scan() {
			if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
				return data, true
			}
		}
		return "", false
	}
	for len(got) < len(want) {
		line, ok := next()
		if !ok {
			t.Fatalf("stream ended after %q", got)
		}
		got = append(got, line)
	}
	if _, err := s.Close(ctx, logseg.End{}); err != nil {
		t.Fatal(err)
	}
	for {
		line, ok := next()
		if !ok {
			break
		}
		if line != "{}" {
			got = append(got, line)
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("stream sent %q, want each of %q once", got, want)
	}
}

func TestNodeLogReadsThroughTheControllerRoutes(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	tok := f.claim(t, "a", store.ClaimTokenWork)
	s, _ := f.sink(t, tok, "a")
	for _, line := range []string{"alpha\n", "needle one\n", "beta\n", "needle two\n"} {
		if err := s.Append([]byte(line), "", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Close(ctx, logseg.End{}); err != nil {
		t.Fatal(err)
	}
	logs := client.NewWithToken(f.url, nil, f.admin(t)).NodeLogs()
	if got, err := logs.Read(ctx, "run-d", "a", storage.ReadOpts{Tail: 1}); err != nil || string(got) != "needle two\n" {
		t.Fatalf("tail read = %q, %v", got, err)
	}
	matches, err := logs.Grep(ctx, "run-d", "a", "needle", 0)
	if err != nil || len(matches) != 2 || matches[0].LineNo != 2 || matches[1].LineNo != 4 {
		t.Fatalf("grep = %+v, %v", matches, err)
	}
	if one, err := logs.Grep(ctx, "run-d", "a", "needle", 1); err != nil || len(one) != 1 {
		t.Fatalf("grep capped at one = %+v, %v", one, err)
	}
}

func TestCompileLogTakesSegmentsUnderTheTriggerClaim(t *testing.T) {
	f := newLogSegmentFixture(t)
	admin := f.admin(t)
	post := func(header bool) (int, []byte) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-d/nodes/_compile/log-segments", strings.NewReader(`{"next":1,"count":1}`))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/json")
		if header {
			req.Header.Set(store.TriggerGenerationHeader, "3")
		}
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	if code, body := post(false); code != http.StatusNotFound {
		t.Fatalf("compile log without a trigger claim: %d %s", code, body)
	}
	code, body := post(true)
	if code != http.StatusOK || !strings.Contains(string(body), "/runs/run-d/_5fcompile/a1-g3/") {
		t.Fatalf("compile log under the trigger claim: %d %s", code, body)
	}
}

func TestCapabilitiesAdvertiseLogSegments(t *testing.T) {
	f := newLogSegmentFixture(t)
	keeps, err := client.NewWithToken(f.url, nil, "").KeepsLogSegments(t.Context())
	if err != nil || !keeps {
		t.Fatalf("KeepsLogSegments = %v, %v", keeps, err)
	}
}

// A viewer holding a node's stream open marks it watched; the node's
// claim heartbeat and its run heartbeat report it, and a heartbeat that
// does not ask keeps its old 204.
func TestHeartbeatsReportWhatAViewerWatches(t *testing.T) {
	f := newLogSegmentFixture(t)
	tok := f.claim(t, "a", store.ClaimTokenWork)
	beat := func() bool {
		code, raw := f.do(t, http.MethodPost, "/api/v1/runs/run-d/nodes/a/heartbeat", tok, []byte(`{"lease_secs":60}`))
		var out struct {
			Watched bool `json:"watched"`
		}
		if code != http.StatusOK || json.Unmarshal(raw, &out) != nil {
			t.Fatalf("claim heartbeat: %d %s", code, raw)
		}
		return out.Watched
	}
	if beat() {
		t.Fatal("an unviewed node reads watched")
	}
	f.srv.liveLogs.Watch("run-d", "a")
	if !beat() {
		t.Fatal("a viewed node reads unwatched")
	}

	admin := f.admin(t)
	runBeat := func(ask bool) (int, []byte) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-d/heartbeat", nil)
		req.Header.Set("Authorization", "Bearer "+admin)
		if ask {
			req.Header.Set(store.LogWatchHeader, "1")
		}
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	if code, body := runBeat(false); code != http.StatusNoContent {
		t.Fatalf("run heartbeat that did not ask: %d %s", code, body)
	}
	if code, body := runBeat(true); code != http.StatusOK || !strings.Contains(string(body), `"watched_nodes":["a"]`) {
		t.Fatalf("run heartbeat that asked: %d %s", code, body)
	}
}

// A log the run store does not record, as the logs service's export
// writes, reads and deletes like one a runner wrote.
func TestUnrecordedLogsReadAndDelete(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	objs := f.srv.logObjects()
	prefix, err := logseg.AttemptPrefix(logseg.ClassFree, string(store.DefaultTeam), "run-d", "b", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("exported line\n"))
	_ = zw.Close()
	sum := sha256.Sum256(gz.Bytes())
	if err := objs.putIfAbsent(ctx, logseg.SegmentKey(prefix, 1), gz.Bytes()); err != nil {
		t.Fatal(err)
	}
	manifest, _ := logseg.Manifest{
		Version: logseg.ManifestVersion, Run: "run-d", Node: "b", Attempt: 1, Status: logseg.StatusComplete,
		Lines: 1, Segments: []logseg.Segment{{N: 1, FirstSeq: 1, LastSeq: 1, Lines: 1, SHA256: hex.EncodeToString(sum[:])}},
	}.Marshal()
	if err := objs.putIfAbsent(ctx, logseg.ManifestKey(prefix), manifest); err != nil {
		t.Fatal(err)
	}
	logs := f.srv.dashboardLogs(store.DefaultTeam)
	if got, err := logs.Read(ctx, "run-d", "b", storage.ReadOpts{}); err != nil || string(got) != "exported line\n" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if run, err := logs.ReadRun(ctx, "run-d"); err != nil || !strings.Contains(string(run), "=== b ===\nexported line\n") {
		t.Fatalf("read run = %q, %v", run, err)
	}
	if code, raw := f.do(t, http.MethodDelete, "/api/v1/runs/run-d", f.admin(t), nil); code != http.StatusNoContent {
		t.Fatalf("delete run: %d %s", code, raw)
	}
	if left, _ := objs.List(ctx, prefix); len(left) != 0 {
		t.Fatalf("deleting the run left %v", left)
	}
}
