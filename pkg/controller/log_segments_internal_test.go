package controller

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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
	srv := New(st, nil)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	f := logSegmentFixture{dispatchRouteFixture: dispatchRouteFixture{st: st, handler: srv.Handler()}, srv: srv, url: hs.URL}
	plan := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	wantPost(t, f.dispatchRouteFixture, planPath, plan, `{"nodes":[{"id":"a","deps":[],`+dispatchHash+`},{"id":"b","deps":[],`+dispatchHash+`}]}`,
		http.StatusOK, "accepted")
	return f
}

func logLine(i int) string { return fmt.Sprintf("line %02d\n", i) }

func TestCompileLogTakesSegmentsUnderTheTriggerClaim(t *testing.T) {
	f := newLogSegmentFixture(t)
	admin := f.admin(t)
	post := func(header bool) (int, []byte) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-d/nodes/_compile/log-segments", strings.NewReader(`{}`))
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
}

func TestUnrecordedLogObjectsAreNotReadable(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	objs := f.srv.logObjects()
	prefix, err := blob.LogSubjectPrefix(logseg.ClassFree, blob.LogSubject{
		Kind: blob.LogJobAttempt, Team: string(store.DefaultTeam), RunID: "run-d",
		Attempt: &runspec.AttemptIdentity{Team: string(store.DefaultTeam), RunID: "run-d", NodeID: "b", Attempt: 1, Generation: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("exported line\n"))
	_ = zw.Close()
	sum := sha256.Sum256(gz.Bytes())
	if err := objs.putIfAbsent(ctx, blob.LogSegmentKey(prefix, 1), gz.Bytes()); err != nil {
		t.Fatal(err)
	}
	manifest, _ := logseg.Manifest{
		Version: logseg.ManifestVersion, Subject: new(nodeLogSubject(store.NodeLog{Team: store.DefaultTeam, RunID: "run-d", NodeID: "b", Attempt: 1, Generation: 1})), Status: logseg.StatusComplete,
		Lines: 1, Segments: []logseg.Segment{{N: 1, FirstSeq: 1, LastSeq: 1, Lines: 1, SHA256: hex.EncodeToString(sum[:])}},
	}.Marshal()
	if err := objs.putIfAbsent(ctx, blob.LogManifestKey(prefix), manifest); err != nil {
		t.Fatal(err)
	}
	logs := f.srv.dashboardLogs(store.DefaultTeam)
	if got, err := logs.Read(ctx, "run-d", "b", storage.ReadOpts{}); err != nil || len(got) != 0 {
		t.Fatalf("read = %q, %v", got, err)
	}
	if run, err := logs.ReadRun(ctx, "run-d"); err != nil || len(run) != 0 {
		t.Fatalf("read run = %q, %v", run, err)
	}
	if code, raw := f.do(t, http.MethodDelete, "/api/v1/runs/run-d", f.admin(t), nil); code != http.StatusNoContent {
		t.Fatalf("delete run: %d %s", code, raw)
	}
	if left, _ := objs.List(ctx, prefix); len(left) != 0 {
		t.Fatalf("deleting the run left %v", left)
	}
}

// On a multi-team controller signing a segment's URL charges the team's
// log storage exactly the segment's size, once however often the writer
// asks for that segment, and never for other bytes under its number.
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
		var used int64
		if err := f.st.DB().QueryRowContext(t.Context(), "SELECT used_bytes FROM team_storage WHERE team=? AND store=?", store.DefaultTeam, store.StorageLogs).Scan(&used); err != nil {
			t.Fatal(err)
		}
		return used
	}
	for _, step := range []struct {
		n    int
		body string
		code int
		want int64
	}{
		{0, "", http.StatusOK, 0},
		{1, strings.Repeat("a", 500), http.StatusOK, 500},
		{1, strings.Repeat("a", 500), http.StatusOK, 500},
		{1, strings.Repeat("b", 500), http.StatusConflict, 500},
		{2, strings.Repeat("c", 300), http.StatusOK, 800},
	} {
		if code, raw := f.do(t, http.MethodPost, "/api/v1/runs/run-d/nodes/a/log-segments", tok, signRequest(step.n, []byte(step.body))); code != step.code {
			t.Fatalf("sign segment %d: %d %s, want %d", step.n, code, raw, step.code)
		}
		if got := committed(); got != step.want {
			t.Fatalf("after segment %d the team holds %d, want %d", step.n, got, step.want)
		}
	}
}

func signRequest(n int, body []byte) []byte {
	if n == 0 {
		return []byte(`{}`)
	}
	md := md5.Sum(body)
	sum := sha256.Sum256(body)
	raw, _ := json.Marshal(store.LogSegmentRequest{
		N: n, Size: int64(len(body)), MD5: base64.StdEncoding.EncodeToString(md[:]), SHA256: hex.EncodeToString(sum[:]),
	})
	return raw
}

func (f logSegmentFixture) sign(t *testing.T, bearer, nodeID string, n int, body []byte) (int, store.LogSegmentGrant) {
	t.Helper()
	code, raw := f.do(t, http.MethodPost, "/api/v1/runs/run-d/nodes/"+nodeID+"/log-segments", bearer, signRequest(n, body))
	var grant store.LogSegmentGrant
	if code == http.StatusOK {
		if err := json.Unmarshal(raw, &grant); err != nil {
			t.Fatal(err)
		}
	}
	return code, grant
}

// A node may not sign more than its cap of stored bytes, so a holder of
// its URLs cannot fill the team's bucket.
func TestLogSegmentsRefuseSegmentsPastTheNodeCap(t *testing.T) {
	f := newLogSegmentFixture(t)
	tok := f.claim(t, "a", store.ClaimTokenWork)
	chunk := bytes.Repeat([]byte{'x'}, int(limits.LogSegmentUploadBytes))
	var n int
	for stored := int64(0); stored+limits.LogSegmentUploadBytes <= limits.StoredAttemptLogBytes; stored += limits.LogSegmentUploadBytes {
		n++
		chunk[0] = byte(n)
		if code, _ := f.sign(t, tok, "a", n, chunk); code != http.StatusOK {
			t.Fatalf("segment %d within the cap: %d", n, code)
		}
	}
	chunk[0] = byte(n + 1)
	if code, _ := f.sign(t, tok, "a", n+1, chunk); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a segment past the %d-byte cap: %d, want 413", limits.StoredAttemptLogBytes, code)
	}
	if rec, err := f.st.LatestNodeLog(t.Context(), store.DefaultTeam, "run-d", "a"); err != nil || rec.StoredBytes > limits.StoredAttemptLogBytes {
		t.Fatalf("record = %+v, %v; want at most the cap stored", rec, err)
	}
}

// The manifest goes through the controller, which seals the log only with
// segments it signed for exactly those sizes and digests.
func TestLogManifestMustMatchTheSignedSegments(t *testing.T) {
	f := newLogSegmentFixture(t)
	tok := f.claim(t, "a", store.ClaimTokenWork)
	body := []byte("one segment")
	code, grant := f.sign(t, tok, "a", 1, body)
	if code != http.StatusOK {
		t.Fatalf("sign: %d", code)
	}
	sum := sha256.Sum256(body)
	seal := func(segs ...logseg.Segment) int {
		t.Helper()
		raw, err := logseg.Manifest{
			Version: logseg.ManifestVersion, Subject: new(nodeLogSubject(store.NodeLog{Team: store.DefaultTeam, RunID: "run-d", NodeID: "a", Attempt: grant.Attempt, Generation: grant.Generation})),
			Status: logseg.StatusComplete, SealedBy: logseg.SealedByNode, Segments: segs,
		}.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		code, _ := f.do(t, http.MethodPost, "/api/v1/runs/run-d/nodes/a/log-manifest", tok, raw)
		return code
	}
	good := logseg.Segment{N: 1, StoredBytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
	forged := good
	forged.SHA256 = strings.Repeat("0", 64)
	unsigned := logseg.Segment{N: 2, StoredBytes: 5, SHA256: hex.EncodeToString(sum[:])}
	for name, segs := range map[string][]logseg.Segment{
		"another digest":     {forged},
		"another size":       {{N: 1, StoredBytes: 99, SHA256: good.SHA256}},
		"an unsigned number": {good, unsigned},
	} {
		if code := seal(segs...); code != http.StatusUnprocessableEntity {
			t.Fatalf("a manifest naming %s: %d, want 422", name, code)
		}
	}
	if code := seal(good); code != http.StatusNoContent {
		t.Fatalf("the signed manifest: %d, want 204", code)
	}
	if code := seal(good); code != http.StatusPreconditionFailed {
		t.Fatalf("a second manifest: %d, want 412", code)
	}
	if rec, err := f.st.LatestNodeLog(t.Context(), store.DefaultTeam, "run-d", "a"); err != nil || rec.SealedAt.IsZero() {
		t.Fatalf("record after the seal = %+v, %v", rec, err)
	}
	if code, _ := f.sign(t, tok, "a", 2, []byte("late")); code != http.StatusConflict {
		t.Fatalf("a segment after the seal: %d, want 409", code)
	}
}

// A charge the team's storage refuses signs nothing, so the writer's retry
// is refused again rather than granted on a charge that never reached the
// team's count.
func TestARefusedLogChargeStaysRefusedOnRetry(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	now := time.Now()
	rec, err := f.st.OpenNodeLog(ctx, store.NodeLog{Team: "slotless", RunID: "run-x", NodeID: "a", Attempt: 1, Generation: 1, Prefix: "logs/r30/teams/slotless/runs/run-x/jobs/a/a1-g1/"}, now)
	if err != nil {
		t.Fatal(err)
	}
	seg := store.NodeLogSegment{N: 1, StoredBytes: 100, MD5: "m", SHA256: "s"}
	for try := 1; try <= 2; try++ {
		if err := f.st.SignNodeLogSegment(ctx, rec, seg, limits.StoredAttemptLogBytes, true, now); err == nil {
			t.Fatalf("charge %d for a team with no storage succeeded", try)
		}
		got, err := f.st.GetNodeLog(ctx, rec.Team, rec.RunID, rec.NodeID, rec.Attempt, rec.Generation)
		if err != nil {
			t.Fatal(err)
		}
		if got.ChargedBytes != 0 || got.StoredBytes != 0 {
			t.Fatalf("after refused charge %d the attempt records %+v, want nothing", try, got)
		}
	}
	if segs, err := f.st.NodeLogSegments(ctx, rec); err != nil || len(segs) != 0 {
		t.Fatalf("a refused charge signed %+v, %v", segs, err)
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

func sealedAttempt(t *testing.T, f logSegmentFixture, runID string, attempt, generation int64, body string) {
	t.Helper()
	ctx := t.Context()
	objs := f.srv.logObjects()
	prefix, err := blob.LogSubjectPrefix(logseg.ClassStandard, blob.LogSubject{
		Kind: blob.LogJobAttempt, Team: string(store.DefaultTeam), RunID: runID,
		Attempt: &runspec.AttemptIdentity{Team: string(store.DefaultTeam), RunID: runID, NodeID: "a", Attempt: attempt, Generation: generation},
	})
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
	if err := objs.putIfAbsent(ctx, blob.LogSegmentKey(prefix, 1), gz.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := logseg.SealAbandoned(ctx, objs, prefix, logseg.Manifest{Subject: new(nodeLogSubject(store.NodeLog{Team: store.DefaultTeam, RunID: runID, NodeID: "a", Attempt: attempt, Generation: generation}))},
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
	admin, _, err := f.st.CreateToken("root", store.TokenKindUser, []string{"admin"}, 0, time.Now())
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
