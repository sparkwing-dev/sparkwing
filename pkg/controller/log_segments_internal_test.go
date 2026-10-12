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
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
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
		Live: func(off int64, data []byte) { f.srv.liveLogs.AppendAt("run-d", "a", up.LiveAttempt(), off, data) },
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
	if _, err := f.st.DB().ExecContext(ctx, `UPDATE nodes SET status = 'done' WHERE run_id = 'run-d' AND node_id = 'a'`); err != nil {
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

// A log the run store does not record reads and deletes like one a runner
// wrote.
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

// On a multi-team controller a writer's reported stored bytes count
// against its team's log storage once each, however often it reports
// them.
func TestLogSegmentsChargeTheTeamsLogStorageOnce(t *testing.T) {
	f := newLogSegmentFixture(t)
	pub, priv := licensetest.NewKey(t)
	now := time.Now()
	f.srv.WithLicense(license.Resolve(licensetest.Sign(t, priv, licensetest.Terms{
		Features: []string{license.FeatureMultiTeam}, IssuedTo: "test",
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour),
	}), pub, now, nil))
	tok := f.claim(t, "a", store.ClaimTokenWork)
	committed := func() int64 {
		marks, err := f.st.StorageMarks(t.Context(), store.StorageLogs)
		if err != nil {
			t.Fatal(err)
		}
		return marks[store.DefaultTeam]
	}
	for _, step := range []struct {
		stored int64
		want   int64
	}{{0, 0}, {500, 500}, {500, 500}, {800, 800}} {
		body := fmt.Sprintf(`{"next":1,"count":1,"stored_bytes":%d}`, step.stored)
		if code, raw := f.do(t, http.MethodPost, "/api/v1/runs/run-d/nodes/a/log-segments", tok, []byte(body)); code != http.StatusOK {
			t.Fatalf("log-segments: %d %s", code, raw)
		}
		if got := committed(); got != step.want {
			t.Fatalf("after reporting %d stored bytes the team holds %d, want %d", step.stored, got, step.want)
		}
	}
}

// The storage pass counts what a team's log segments hold, from a
// listing of the bucket, so a count left wrong by a failed commit heals.
func TestStoragePassCountsLogSegments(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	tok := f.claim(t, "a", store.ClaimTokenWork)
	s, _ := f.sink(t, tok, "a")
	for i := 1; i <= 4; i++ {
		if err := s.Append([]byte(logLine(i)), "", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Close(ctx, logseg.End{}); err != nil {
		t.Fatal(err)
	}
	counted, err := f.srv.logObjects().measure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := f.st.LatestNodeLog(ctx, store.DefaultTeam, "run-d", "a")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := f.srv.logObjects().List(ctx, rec.Prefix)
	if err != nil || len(keys) < 2 {
		t.Fatalf("objects = %v, %v", keys, err)
	}
	var want int64
	for _, k := range keys {
		body, err := f.srv.logObjects().Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := io.Copy(io.Discard, body)
		_ = body.Close()
		want += n
	}
	if counted[store.DefaultTeam] != want {
		t.Fatalf("measured %d bytes for the team, want %d", counted[store.DefaultTeam], want)
	}
	f.srv.WithStoragePass(nil)
	if ran, err := f.srv.RunStoragePass(ctx); err != nil || !ran {
		t.Fatalf("storage pass = %v, %v", ran, err)
	}
	usage, err := f.st.TeamStorage(ctx, store.DefaultTeam)
	if err != nil || usage[store.StorageLogs].UsedBytes != want {
		t.Fatalf("reconciled log storage = %+v, %v; want %d used", usage[store.StorageLogs], err, want)
	}
}

// A team past its free log share loses its finished runs' segments and
// records, and never a run still going.
func TestPruningATeamOverItsShareDeletesOnlyFinishedRunsSegments(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	tok := f.claim(t, "a", store.ClaimTokenWork)
	s, _ := f.sink(t, tok, "a")
	for i := 1; i <= 4; i++ {
		if err := s.Append([]byte(logLine(i)), "", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Close(ctx, logseg.End{}); err != nil {
		t.Fatal(err)
	}
	rec, err := f.st.LatestNodeLog(ctx, store.DefaultTeam, "run-d", "a")
	if err != nil {
		t.Fatal(err)
	}
	objs := f.srv.logObjects()
	over := store.TeamOverShare{Team: store.DefaultTeam, OverBytes: 1}
	if _, err := f.srv.pruneTeamSegments(ctx, objs, over); err != nil {
		t.Fatal(err)
	}
	if keys, err := objs.List(ctx, rec.Prefix); err != nil || len(keys) == 0 {
		t.Fatalf("a running run's segments after the prune = %v, %v; want them kept", keys, err)
	}
	if _, err := f.st.DB().ExecContext(ctx, `UPDATE runs SET status = 'success' WHERE id = 'run-d'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.pruneTeamSegments(ctx, objs, over); err != nil {
		t.Fatal(err)
	}
	if keys, err := objs.List(ctx, rec.Prefix); err != nil || len(keys) != 0 {
		t.Fatalf("a finished run's segments after the prune = %v, %v; want none", keys, err)
	}
	if recs, err := f.st.RunNodeLogs(ctx, store.DefaultTeam, "run-d"); err != nil || len(recs) != 0 {
		t.Fatalf("log records after the prune = %v, %v; want none", recs, err)
	}
}

// A sealed log that lost a segment, or holds other bytes under its key,
// must not read, or report, as whole: the reader gets the lines before
// the gap and a loss marker, and completeness says incomplete.
func TestALostSegmentReadsAndReportsAsIncomplete(t *testing.T) {
	for name, damage := range map[string]func(context.Context, fsLogObjects, string) error{
		"deleted": func(ctx context.Context, objs fsLogObjects, key string) error {
			_, err := objs.deletePrefix(ctx, key)
			return err
		},
		"replaced": func(ctx context.Context, objs fsLogObjects, key string) error {
			return objs.objs.Put(ctx, key, strings.NewReader("not the segment the manifest names"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newLogSegmentFixture(t)
			ctx := t.Context()
			tok := f.claim(t, "a", store.ClaimTokenWork)
			s, _ := f.sink(t, tok, "a")
			for i := 1; i <= 4; i++ {
				if err := s.Append([]byte(logLine(i)), "", 0, 0); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Close(ctx, logseg.End{}); err != nil {
				t.Fatal(err)
			}
			rec, err := f.st.LatestNodeLog(ctx, store.DefaultTeam, "run-d", "a")
			if err != nil {
				t.Fatal(err)
			}
			objs := f.srv.logObjects()
			if err := damage(ctx, objs.(fsLogObjects), logseg.SegmentKey(rec.Prefix, 2)); err != nil {
				t.Fatal(err)
			}
			logs := segmentLogs{s: f.srv, team: store.DefaultTeam, objs: objs}
			body, err := logs.Read(ctx, "run-d", "a", storage.ReadOpts{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), logLine(1)) || strings.Contains(string(body), logLine(4)) ||
				!strings.Contains(string(body), "segment 2 is missing") {
				t.Fatalf("read of a log with segment 2 %s = %q, want line 1 then a loss marker", name, body)
			}
			c, found, err := logs.NodeLogCompleteness(ctx, "run-d", "a", logseg.NodeProgress{Started: true}, time.Now())
			if err != nil || !found || c.State != logseg.StateIncomplete {
				t.Fatalf("completeness of a log with segment 2 %s = %+v, %v, %v; want incomplete", name, c, found, err)
			}
		})
	}
}

// A charge the team's storage refuses leaves the attempt uncharged, so the
// writer's retry is refused again rather than granted on the strength of
// a checkpoint that never reached the team's count.
func TestARefusedLogChargeStaysRefusedOnRetry(t *testing.T) {
	f := newLogSegmentFixture(t)
	pub, priv := licensetest.NewKey(t)
	now := time.Now()
	f.srv.WithLicense(license.Resolve(licensetest.Sign(t, priv, licensetest.Terms{
		Features: []string{license.FeatureMultiTeam}, IssuedTo: "test",
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour),
	}), pub, now, nil))
	ctx := t.Context()
	rec, err := f.st.OpenNodeLog(ctx, store.NodeLog{Team: "slotless", RunID: "run-x", NodeID: "a", Attempt: 1, Generation: 1, Prefix: "logs/r30/teams/slotless/runs/run-x/a/a1-g1/"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for try := 1; try <= 2; try++ {
		if err := f.srv.chargeNodeLog(ctx, rec, 100, now); err == nil {
			t.Fatalf("charge %d for a team with no storage succeeded", try)
		}
		got, err := f.st.GetNodeLog(ctx, rec.Team, rec.RunID, rec.NodeID, rec.Attempt, rec.Generation)
		if err != nil {
			t.Fatal(err)
		}
		if got.ChargedBytes != 0 {
			t.Fatalf("after refused charge %d the attempt records %d charged bytes, want 0", try, got.ChargedBytes)
		}
		rec = got
	}
}

// A team's logs written under a retention class the controller no longer
// configures still go when the team is deleted, and at its recheck, after
// the purge removed the records that named the class.
func TestTeamDeletionReachesARetiredRetentionClass(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	f.srv.WithLogRetention(map[store.Team]int{store.DefaultTeam: 365})
	tok := f.claim(t, "a", store.ClaimTokenWork)
	s, _ := f.sink(t, tok, "a")
	if err := s.Append([]byte(logLine(1)), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Close(ctx, logseg.End{}); err != nil {
		t.Fatal(err)
	}
	rec, err := f.st.LatestNodeLog(ctx, store.DefaultTeam, "run-d", "a")
	if err != nil || !strings.HasPrefix(rec.Prefix, "logs/r365/") {
		t.Fatalf("record = %+v, %v; want the r365 class", rec, err)
	}
	f.srv.WithLogRetention(nil)
	if err := f.st.DeleteRunNodeLogs(ctx, store.DefaultTeam, "run-d"); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.purgeTeamLogs(ctx, store.DefaultTeam); err != nil {
		t.Fatal(err)
	}
	if left, err := f.srv.logObjects().List(ctx, rec.Prefix); err != nil || len(left) != 0 {
		t.Fatalf("after the purge the retired class still holds %v, %v", left, err)
	}
}

// A new attempt's sink numbers its bytes from zero again, so the live ring
// must start over for it instead of taking its first lines for the old
// attempt's and dropping them.
func TestTheLiveRingStartsOverForANewAttempt(t *testing.T) {
	f := newLogSegmentFixture(t)
	admin := f.admin(t)
	push := func(attempt, line string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-d/nodes/a/logs", strings.NewReader(line))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Sparkwing-Log-Offset", "0")
		req.Header.Set("X-Sparkwing-Log-Attempt", attempt)
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("live push = %d %s", rec.Code, rec.Body.String())
		}
	}
	push("a1-g1", "first attempt\n")
	push("a2-g2", "second attempt\n")
	chunk, ok := f.srv.liveLogs.Read("run-d", "a", 0)
	if !ok || string(chunk.Data) != "second attempt\n" {
		t.Fatalf("live ring after a second attempt = %q, %v; want only the second attempt's line", chunk.Data, ok)
	}
}

// Pruning stops once the bytes it actually deleted cover the overage, even
// for attempts never charged: the oldest finished run goes, the next stays.
func TestPruningCountsTheBytesItDeletes(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	objs := f.srv.logObjects()
	base := time.Now().Add(-time.Hour)
	prefixes := map[string]string{}
	for i, runID := range []string{"r-old", "r-new"} {
		at := base.Add(time.Duration(i) * time.Minute)
		if err := f.st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "success", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		prefix := "logs/r90/teams/default/runs/" + runID + "/a/a1-g1/"
		if _, err := f.st.OpenNodeLog(ctx, store.NodeLog{Team: store.DefaultTeam, RunID: runID, NodeID: "a", Attempt: 1, Generation: 1, Prefix: prefix}, at); err != nil {
			t.Fatal(err)
		}
		if err := objs.putIfAbsent(ctx, logseg.SegmentKey(prefix, 1), bytes.Repeat([]byte("x"), 100)); err != nil {
			t.Fatal(err)
		}
		prefixes[runID] = prefix
	}
	freed, err := f.srv.pruneTeamSegments(ctx, objs, store.TeamOverShare{Team: store.DefaultTeam, OverBytes: 50})
	if err != nil {
		t.Fatal(err)
	}
	if freed != 100 {
		t.Fatalf("freed %d bytes, want the 100 the oldest run held", freed)
	}
	for runID, want := range map[string]int{"r-old": 0, "r-new": 1} {
		if left, err := objs.List(ctx, prefixes[runID]); err != nil || len(left) != want {
			t.Fatalf("%s holds %v after the prune, %v; want %d object(s)", runID, left, err, want)
		}
	}
}

func sealedAttempt(t *testing.T, f logSegmentFixture, runID string, attempt, generation int64, body string) {
	t.Helper()
	ctx := t.Context()
	objs := f.srv.logObjects()
	prefix, err := logseg.AttemptPrefix(logseg.ClassStandard, string(store.DefaultTeam), runID, "a", attempt, generation)
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := objs.putIfAbsent(ctx, logseg.SegmentKey(prefix, 1), gz.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := logseg.SealAbandoned(ctx, objs, prefix, logseg.Manifest{Run: runID, Node: "a", Attempt: attempt, Generation: generation},
		logseg.ReasonRunnerLost, objs.putIfAbsent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.OpenNodeLog(ctx, store.NodeLog{
		Team: store.DefaultTeam, RunID: runID, NodeID: "a",
		Attempt: attempt, Generation: generation, Prefix: prefix,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// A node's stream runs through every attempt of it in this run, in order --
// a replacement claim of the first attempt and a retry that started and
// finished between two polls among them -- leaves out the attempts of the
// run it retries, ends once the node is done, and resumes from an event id
// without sending a line twice.
func TestNodeLogStreamFollowsEveryAttemptAndResumes(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	sealedAttempt(t, f, "run-ancestor", 1, 1, "ancestor line\n")
	sealedAttempt(t, f, "run-d", 1, 1, "first claim\nsecond line\n")
	sealedAttempt(t, f, "run-d", 1, 2, "replacement claim\n")
	sealedAttempt(t, f, "run-d", 2, 3, "retry\n")
	if _, err := f.st.DB().ExecContext(ctx, `UPDATE nodes SET status = 'done' WHERE run_id = 'run-d' AND node_id = 'a'`); err != nil {
		t.Fatal(err)
	}
	admin, _, err := f.st.CreateToken("root", store.TokenKindUser, []string{ScopeAdmin}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stream := func(cursor string, query ...string) (int, []string, []string) {
		u := f.url + "/api/v1/runs/run-d/logs/a/stream?format=raw"
		for _, q := range query {
			u += "&cursor=" + q
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+admin)
		if cursor != "" {
			req.Header.Set("Last-Event-ID", cursor)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var lines, ids []string
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			switch line := sc.Text(); {
			case strings.HasPrefix(line, "data: "):
				lines = append(lines, strings.TrimPrefix(line, "data: "))
			case strings.HasPrefix(line, "id: "):
				ids = append(ids, strings.TrimPrefix(line, "id: "))
			}
		}
		return resp.StatusCode, lines, ids
	}
	code, lines, ids := stream("")
	if code != http.StatusOK || strings.Join(lines, ",") != "first claim,second line,replacement claim,retry,{}" {
		t.Fatalf("stream = %d %q; want each attempt of this run in order, then the end", code, lines)
	}
	if len(ids) < 3 {
		t.Fatalf("event ids = %q; want one on each line", ids)
	}
	if _, resumed, _ := stream(ids[0]); strings.Join(resumed, ",") != "second line,replacement claim,retry,{}" {
		t.Fatalf("resumed from the first line's id %s = %q; want the lines after it alone", ids[0], resumed)
	}
	if _, resumed, _ := stream(ids[0], ids[2]); strings.Join(resumed, ",") != "second line,replacement claim,retry,{}" {
		t.Fatalf("resumed with Last-Event-ID %s and an older-looking query cursor = %q; want the header to win", ids[0], resumed)
	}
	if code, _, _ := stream("0.0.00000000"); code != http.StatusBadRequest {
		t.Fatalf("a cursor for another stream = %d, want 400", code)
	}
}

// A failed node may be retried until its run ends, whatever its recorded
// budget says: one that sits done during a dispatcher's retry backoff with
// no budget recorded, or one that failed inline with nothing consumed,
// keeps its log open until the run is over. A node done any other way
// settles at once.
func TestNodeLogSettlesOnlyWithNoRetryToCome(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	set := func(q string) {
		t.Helper()
		if _, err := f.st.DB().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	settled := func() bool {
		t.Helper()
		got, found, err := f.st.NodeLogSettled(ctx, store.DefaultTeam, "run-d", "a")
		if err != nil || !found {
			t.Fatalf("settled = %v, %v, %v", got, found, err)
		}
		return got
	}
	for name, q := range map[string]string{
		"retry backoff with no budget recorded": `UPDATE nodes SET status = 'done', outcome = 'failed', attempts_consumed = 1, retry_budget = 0 WHERE run_id = 'run-d' AND node_id = 'a'`,
		"inline failure":                        `UPDATE nodes SET status = 'done', outcome = 'failed', attempts_consumed = 0, retry_budget = 0 WHERE run_id = 'run-d' AND node_id = 'a'`,
	} {
		set(q)
		if settled() {
			t.Fatalf("%s: a failed node of a running run reads as settled", name)
		}
	}
	set(`UPDATE runs SET status = 'failed' WHERE id = 'run-d'`)
	if !settled() {
		t.Fatal("a failed node of a finished run reads as unsettled")
	}
	set(`UPDATE runs SET status = 'running' WHERE id = 'run-d'`)
	for _, outcome := range []string{"success", "skipped", "cancelled"} {
		set(`UPDATE nodes SET outcome = '` + outcome + `' WHERE run_id = 'run-d' AND node_id = 'a'`)
		if !settled() {
			t.Fatalf("a node done with outcome %s reads as unsettled", outcome)
		}
	}
}
