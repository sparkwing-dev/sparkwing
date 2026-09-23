package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func (f *archiveFixture) send(t *testing.T, method, path, bearer, body string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.http.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", bearer)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (f *archiveFixture) appendLine(t *testing.T, bearer, run, node, stream string, seq int64) {
	t.Helper()
	headers := map[string]string{}
	if stream != "" {
		headers[LogStreamHeader] = stream
		headers[LogSeqHeader] = fmt.Sprint(seq)
	}
	code, body := f.send(t, http.MethodPost, "/api/v1/logs/"+run+"/"+node, bearer, fmt.Sprintf("line %d\n", seq), headers)
	if code != http.StatusNoContent {
		t.Fatalf("append seq %d = %d %s", seq, code, body)
	}
}

func (f *archiveFixture) seal(t *testing.T, bearer, run, node string, seal Seal) int {
	t.Helper()
	raw, _ := json.Marshal(seal)
	code, _ := f.send(t, http.MethodPost, "/api/v1/logs/"+run+"/"+node+"/seal", bearer, string(raw), nil)
	return code
}

func (f *archiveFixture) report(t *testing.T, bearer, run, node string) SealReport {
	t.Helper()
	code, body := f.send(t, http.MethodGet, "/api/v1/logs/"+run+"/"+node+"/seal", bearer, "", nil)
	if code != http.StatusOK {
		t.Fatalf("read seals = %d %s", code, body)
	}
	var r SealReport
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return r
}

var (
	finished = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	done     = NodeProgress{Started: true, Terminal: true, FinishedAt: finished}
	late     = finished.Add(SealGrace + time.Second)
)

func TestSealedStreamWithEveryLineReadsComplete(t *testing.T) {
	f := newArchiveFixture(t, 0)
	for seq := int64(1); seq <= 3; seq++ {
		f.appendLine(t, "Bearer a", "run-a", "build", "s1", seq)
	}
	if code := f.seal(t, "Bearer a", "run-a", "build", Seal{Stream: "s1", FinalSeq: 3, Lines: 3, Bytes: 21}); code != http.StatusNoContent {
		t.Fatalf("seal = %d", code)
	}
	got := f.report(t, "Bearer a", "run-a", "build").Assess(done, late)
	if got.State != StateComplete || got.Lines != 3 || got.SyntheticLine() != "" {
		t.Fatalf("sealed complete stream = %+v", got)
	}

	// Negative control: the same stream without its seal is cut off.
	for seq := int64(1); seq <= 3; seq++ {
		f.appendLine(t, "Bearer a", "run-b", "build", "s1", seq)
	}
	if got := f.report(t, "Bearer a", "run-b", "build").Assess(done, late); got.State != StateCutOff {
		t.Fatalf("unsealed stream = %+v, want cut_off", got)
	}
}

func TestSealedStreamWithAGapReadsIncomplete(t *testing.T) {
	f := newArchiveFixture(t, 0)
	for _, seq := range []int64{1, 2, 4, 5} {
		f.appendLine(t, "Bearer a", "run-a", "build", "s1", seq)
	}
	if code := f.seal(t, "Bearer a", "run-a", "build", Seal{Stream: "s1", FinalSeq: 6, Lines: 6}); code != http.StatusNoContent {
		t.Fatalf("seal = %d", code)
	}
	r := f.report(t, "Bearer a", "run-a", "build")
	if len(r.Streams) != 1 || r.Streams[0].Missing != 2 || fmt.Sprint(r.Streams[0].Gaps) != "[{3 3} {6 6}]" {
		t.Fatalf("stream report = %+v", r.Streams)
	}
	got := r.Assess(done, late)
	if got.State != StateIncomplete || got.MissingLines != 2 || got.SyntheticLine() != "— logs incomplete: 2 lines missing —" {
		t.Fatalf("gapped stream = %+v %q", got, got.SyntheticLine())
	}

	// Negative control: the late line fills its gap, and a retried
	// duplicate changes nothing.
	for _, seq := range []int64{1, 2, 4, 5, 3, 3, 6} {
		f.appendLine(t, "Bearer a", "run-b", "build", "s1", seq)
	}
	if code := f.seal(t, "Bearer a", "run-b", "build", Seal{Stream: "s1", FinalSeq: 6, Lines: 6}); code != http.StatusNoContent {
		t.Fatalf("seal = %d", code)
	}
	if got := f.report(t, "Bearer a", "run-b", "build").Assess(done, late); got.State != StateComplete {
		t.Fatalf("filled stream = %+v, want complete", got)
	}
}

func TestRunnerReportedDropsReadIncomplete(t *testing.T) {
	f := newArchiveFixture(t, 0)
	for seq := int64(1); seq <= 3; seq++ {
		f.appendLine(t, "Bearer a", "run-a", "build", "s1", seq)
	}
	if code := f.seal(t, "Bearer a", "run-a", "build", Seal{Stream: "s1", FinalSeq: 3, Lines: 3, Dropped: 1}); code != http.StatusNoContent {
		t.Fatalf("seal = %d", code)
	}
	got := f.report(t, "Bearer a", "run-a", "build").Assess(done, late)
	if got.State != StateIncomplete || got.MissingLines != 1 || got.Message != "logs incomplete: 1 line missing" {
		t.Fatalf("dropping stream = %+v", got)
	}

	// Negative control: a seal that reports no drops reads complete.
	for seq := int64(1); seq <= 3; seq++ {
		f.appendLine(t, "Bearer a", "run-b", "build", "s1", seq)
	}
	f.seal(t, "Bearer a", "run-b", "build", Seal{Stream: "s1", FinalSeq: 3, Lines: 3})
	if got := f.report(t, "Bearer a", "run-b", "build").Assess(done, late); got.State != StateComplete {
		t.Fatalf("clean seal = %+v", got)
	}
}

func TestMissingSealReadsCutOffOnlyAfterTheGrace(t *testing.T) {
	f := newArchiveFixture(t, 0)
	for seq := int64(1); seq <= 1234; seq++ {
		if seq%100 != 1 && seq != 1234 {
			continue
		}
		f.appendLine(t, "Bearer a", "run-a", "build", "s1", seq)
	}
	r := f.report(t, "Bearer a", "run-a", "build")

	// Negative controls: running, and finished inside the grace.
	running := NodeProgress{Started: true}
	if got := r.Assess(running, late); got.State != StateStreaming || got.Message != "" {
		t.Fatalf("running node = %+v", got)
	}
	if got := r.Assess(done, finished.Add(SealGrace-time.Second)); got.State != StateStreaming {
		t.Fatalf("inside the grace = %+v", got)
	}

	got := r.Assess(done, late)
	want := fmt.Sprintf("— logs cut off: the log stream ended without the runner's confirmation after line %s —", groupDigits(r.Lines))
	if got.State != StateCutOff || got.SyntheticLine() != want {
		t.Fatalf("after the grace = %+v %q", got, got.SyntheticLine())
	}
	if groupDigits(1234) != "1,234" || groupDigits(1234567) != "1,234,567" || groupDigits(12) != "12" {
		t.Fatal("digit grouping")
	}
}

func TestWriterWithoutSealSupportReadsUnconfirmed(t *testing.T) {
	f := newArchiveFixture(t, 0)
	f.appendLine(t, "Bearer a", "run-a", "build", "", 1)
	f.appendLine(t, "Bearer a", "run-a", "build", "", 2)
	got := f.report(t, "Bearer a", "run-a", "build").Assess(done, late)
	if got.State != StateUnconfirmed || !strings.Contains(got.Message, "2 lines stored") {
		t.Fatalf("old writer = %+v", got)
	}

	// Negative control: a writer that numbers its lines and never seals
	// is cut off, not unconfirmed.
	f.appendLine(t, "Bearer a", "run-b", "build", "s1", 1)
	if got := f.report(t, "Bearer a", "run-b", "build").Assess(done, late); got.State != StateCutOff {
		t.Fatalf("numbering writer = %+v", got)
	}
}

func TestSealForAnotherTeamsRunIsRefused(t *testing.T) {
	f := newArchiveFixture(t, 0)
	f.appendLine(t, "Bearer a", "run-a", "build", "s1", 1)
	if code := f.seal(t, "Bearer b", "run-a", "build", Seal{Stream: "s1", FinalSeq: 1, Lines: 1}); code/100 == 2 {
		t.Fatalf("team B sealed team A's run: %d", code)
	}
	if r := f.report(t, "Bearer a", "run-a", "build"); r.Streams[0].Sealed {
		t.Fatalf("a refused seal was recorded: %+v", r.Streams)
	}
	if code, _ := f.send(t, http.MethodGet, "/api/v1/logs/run-a/build/seal", "Bearer b", "", nil); code != http.StatusNotFound {
		t.Fatalf("team B read team A's seals: %d", code)
	}
	// Negative control: the owning team's seal lands.
	if code := f.seal(t, "Bearer a", "run-a", "build", Seal{Stream: "s1", FinalSeq: 1, Lines: 1}); code != http.StatusNoContent {
		t.Fatalf("team A seal = %d", code)
	}
	if r := f.report(t, "Bearer a", "run-a", "build"); !r.Streams[0].Sealed {
		t.Fatalf("owner's seal missing: %+v", r.Streams)
	}
}

func TestSealSurvivesTheArchive(t *testing.T) {
	f := newArchiveFixture(t, 0)
	for seq := int64(1); seq <= 3; seq++ {
		f.appendLine(t, "Bearer a", "run-a", "build", "s1", seq)
	}
	if code := f.seal(t, "Bearer a", "run-a", "build", Seal{Stream: "s1", FinalSeq: 4, Lines: 4, Dropped: 1, SHA256: "abc"}); code != http.StatusNoContent {
		t.Fatalf("seal = %d", code)
	}
	if n, err := f.srv.ArchiveOnce(context.Background(), time.Now().Add(DefaultArchiveIdle+time.Minute)); err != nil || n != 1 {
		t.Fatalf("archive = %d, %v", n, err)
	}
	if f.onVolume("run-a") {
		t.Fatal("run stayed on the volume")
	}
	if !f.keys(t)["logs/teams/team-a/runs/run-a/.seals/build.log"] {
		t.Fatalf("seal file not archived: %v", f.keys(t))
	}
	r := f.report(t, "Bearer a", "run-a", "build")
	if len(r.Streams) != 1 || !r.Streams[0].Sealed || r.Streams[0].Seal.SHA256 != "abc" || r.Streams[0].Missing != 1 {
		t.Fatalf("restored report = %+v", r.Streams)
	}
	if got := r.Assess(done, late); got.State != StateIncomplete || got.MissingLines != 1 {
		t.Fatalf("restored verdict = %+v", got)
	}
	// The seal file is metadata: the node's log and the run read hold only log lines.
	if _, body := f.do(t, http.MethodGet, "/api/v1/logs/run-a/build", "Bearer a", ""); body != "line 1\nline 2\nline 3\n" {
		t.Fatalf("node log = %q", body)
	}
	if _, body := f.do(t, http.MethodGet, "/api/v1/logs/run-a", "Bearer a", ""); strings.Contains(body, "seal") || strings.Contains(body, "stream") {
		t.Fatalf("run read leaked seal records: %q", body)
	}
}

func TestSealIsIdempotentAndValidated(t *testing.T) {
	f := newArchiveFixture(t, 0)
	f.appendLine(t, "Bearer a", "run-a", "build", "s1", 1)
	for range 2 {
		if code := f.seal(t, "Bearer a", "run-a", "build", Seal{Stream: "s1", FinalSeq: 1, Lines: 1}); code != http.StatusNoContent {
			t.Fatalf("seal = %d", code)
		}
	}
	if r := f.report(t, "Bearer a", "run-a", "build"); len(r.Streams) != 1 {
		t.Fatalf("repeated seal = %+v", r.Streams)
	}
	for _, bad := range []Seal{{Stream: "", FinalSeq: 1}, {Stream: "../x"}, {Stream: "s1", Dropped: -1}} {
		if code := f.seal(t, "Bearer a", "run-a", "build", bad); code != http.StatusBadRequest {
			t.Errorf("seal %+v = %d, want 400", bad, code)
		}
	}
	if code, _ := f.send(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", "x\n", map[string]string{LogStreamHeader: "s1", LogSeqHeader: "0"}); code != http.StatusBadRequest {
		t.Errorf("seq 0 = %d, want 400", code)
	}
}
