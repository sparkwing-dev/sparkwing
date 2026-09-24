package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

func TestSequenceRangeAccountsForEveryLineAndRejectsFalseRanges(t *testing.T) {
	f := newArchiveFixture(t, 0)
	path := "/api/v1/logs/run-a/build"
	headers := map[string]string{LogStreamHeader: "range", LogSeqHeader: "1", LogSeqEndHeader: "3"}
	for _, bad := range []struct {
		body string
		end  string
	}{
		{"", "2"},
		{"one\ntwo\n", "3"},
		{"one\ntwo\nthree\n", "2"},
		{"one\ntwo\nthree\n", "257"},
		{"one\ntwo\nthree\n", "0"},
	} {
		headers[LogSeqEndHeader] = bad.end
		if code, _ := f.send(t, http.MethodPost, path, "Bearer a", bad.body, headers); code != http.StatusBadRequest {
			t.Fatalf("bad range end %q body %q = %d", bad.end, bad.body, code)
		}
	}
	headers[LogSeqEndHeader] = "3"
	if code, body := f.send(t, http.MethodPost, path, "Bearer a", "one\ntwo\nthree\n", headers); code != http.StatusNoContent {
		t.Fatalf("range append = %d %s", code, body)
	}
	if code := f.seal(t, "Bearer a", "run-a", "build", Seal{Stream: "range", FinalSeq: 3, Lines: 3}); code != http.StatusNoContent {
		t.Fatalf("seal = %d", code)
	}
	if got := f.report(t, "Bearer a", "run-a", "build").Assess(done, late); got.State != StateComplete || got.Lines != 3 {
		t.Fatalf("range verdict = %+v", got)
	}
}

func TestRangeRejectsTrailingUnnumberedRecord(t *testing.T) {
	f := newArchiveFixture(t, 0)
	headers := map[string]string{LogStreamHeader: "range", LogSeqHeader: "1", LogSeqEndHeader: "2"}
	code, _ := f.send(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", "one\ntwo\ntrailing", headers)
	if code != http.StatusBadRequest {
		t.Fatalf("malformed range body = %d, want 400", code)
	}
}

func TestRetryOfCommittedRangeDoesNotDuplicateBody(t *testing.T) {
	f := newArchiveFixture(t, 0)
	path := "/api/v1/logs/run-a/build"
	headers := map[string]string{LogStreamHeader: "range", LogSeqHeader: "1", LogSeqEndHeader: "2"}
	for range 2 {
		if code, body := f.send(t, http.MethodPost, path, "Bearer a", "one\ntwo\n", headers); code != http.StatusNoContent {
			t.Fatalf("append = %d %s", code, body)
		}
	}
	if code, body := f.send(t, http.MethodGet, path, "Bearer a", "", nil); code != http.StatusOK || body != "one\ntwo\n" {
		t.Fatalf("retry returned %d %q, want one copy", code, body)
	}
}

func TestPartialRangeOverlapIsRejectedBeforeWriting(t *testing.T) {
	f := newArchiveFixture(t, 0)
	path := "/api/v1/logs/run-a/build"
	first := map[string]string{LogStreamHeader: "range", LogSeqHeader: "1", LogSeqEndHeader: "2"}
	if code, body := f.send(t, http.MethodPost, path, "Bearer a", "one\ntwo\n", first); code != http.StatusNoContent {
		t.Fatalf("first append = %d %s", code, body)
	}
	overlap := map[string]string{LogStreamHeader: "range", LogSeqHeader: "2", LogSeqEndHeader: "3"}
	if code, _ := f.send(t, http.MethodPost, path, "Bearer a", "two\nthree\n", overlap); code != http.StatusUnprocessableEntity {
		t.Fatalf("overlap = %d, want 422", code)
	}
	if code, body := f.send(t, http.MethodGet, path, "Bearer a", "", nil); code != http.StatusOK || body != "one\ntwo\n" {
		t.Fatalf("after overlap = %d %q", code, body)
	}
}

func TestAmbiguousCommittedRangeRetryStoresOneCopy(t *testing.T) {
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var appends atomic.Int64
	h := s.Handler()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/logs/run/node" && appends.Add(1) == 1 {
			capture := httptest.NewRecorder()
			h.ServeHTTP(capture, r)
			if capture.Code != http.StatusNoContent {
				http.Error(w, "underlying append failed", capture.Code)
				return
			}
			http.Error(w, "upstream lost the acknowledgment", http.StatusBadGateway)
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer hs.Close()
	c := NewClient(hs.URL, nil)
	ctx := WithAppendSequenceRange(context.Background(), "range", 1, 2)
	if err := c.Append(ctx, "run", "node", []byte("one\ntwo\n")); err == nil {
		t.Fatal("first append should report the lost acknowledgment")
	}
	if err := c.Append(ctx, "run", "node", []byte("one\ntwo\n")); err != nil {
		t.Fatal(err)
	}
	if body, err := c.Read(context.Background(), "run", "node"); err != nil || string(body) != "one\ntwo\n" {
		t.Fatalf("stored body = %q, err = %v", body, err)
	}
}

func TestRetriedRangesStayInTheirExecutionAttempt(t *testing.T) {
	f := newArchiveFixture(t, 0)
	path := "/api/v1/logs/run-a/build"
	for _, attempt := range []struct {
		ordinal, start, end, body string
	}{
		{"1", "1", "2", "first\nsecond\n"},
		{"2", "3", "4", "third\nfourth\n"},
	} {
		headers := map[string]string{
			store.ClaimHolderHeader:     "holder",
			store.ClaimGenerationHeader: "1",
			store.AttemptOrdinalHeader:  attempt.ordinal,
			LogStreamHeader:             "same-stream",
			LogSeqHeader:                attempt.start,
			LogSeqEndHeader:             attempt.end,
		}
		for range 2 {
			if code, body := f.send(t, http.MethodPost, path, "Bearer a", attempt.body, headers); code != http.StatusNoContent {
				t.Fatalf("attempt %s append = %d %s", attempt.ordinal, code, body)
			}
		}
		selected := path + "?claim_generation=1&attempt=" + attempt.ordinal
		if code, body := f.send(t, http.MethodGet, selected, "Bearer a", "", nil); code != http.StatusOK || body != attempt.body {
			t.Fatalf("attempt %s read = %d %q", attempt.ordinal, code, body)
		}
	}
}

func TestRangeRetryRepairsMissingAttemptOpenRecordWithoutWritingBody(t *testing.T) {
	f := newArchiveFixture(t, 0)
	path := "/api/v1/logs/run-a/build"
	headers := func(ordinal, first, last string) map[string]string {
		return map[string]string{
			store.ClaimHolderHeader:     "holder",
			store.ClaimGenerationHeader: "1",
			store.AttemptOrdinalHeader:  ordinal,
			LogStreamHeader:             "same-stream",
			LogSeqHeader:                first,
			LogSeqEndHeader:             last,
		}
	}
	if code, body := f.send(t, http.MethodPost, path, "Bearer a", "first\nsecond\n", headers("1", "1", "2")); code != http.StatusNoContent {
		t.Fatalf("first attempt = %d %s", code, body)
	}
	sealPath := filepath.Join(f.root, "runs", nodeSealPath("run-a", "build"))
	saved := sealPath + ".held"
	if err := os.Rename(sealPath, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sealPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.send(t, http.MethodPost, path, "Bearer a", "third\nfourth\n", headers("2", "3", "4")); code != http.StatusInternalServerError {
		t.Fatalf("blocked open record = %d, want 500", code)
	}
	if err := os.Remove(sealPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, sealPath); err != nil {
		t.Fatal(err)
	}
	if code, body := f.send(t, http.MethodPost, path, "Bearer a", "third\nfourth\n", headers("2", "3", "4")); code != http.StatusNoContent {
		t.Fatalf("retry = %d %s", code, body)
	}
	selected := path + "?claim_generation=1&attempt=2"
	if code, body := f.send(t, http.MethodGet, selected, "Bearer a", "", nil); code != http.StatusOK || body != "third\nfourth\n" {
		t.Fatalf("second attempt body = %d %q, want one copy", code, body)
	}
	report := f.report(t, "Bearer a", "run-a", "build")
	wantFile := sealFileRel("run-a", appendIdentity{claimGeneration: 1, attemptOrdinal: 2}.path("run-a", "build"))
	if report.UnconfirmedFiles != 0 || len(report.Streams) != 1 || !slices.Contains(report.Streams[0].Files, wantFile) {
		t.Fatalf("repaired open record missing for %q: %+v", wantFile, report)
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
	if got.State != StateIncomplete || got.MissingLines != 2 || got.SyntheticLine() != "[logs incomplete: 2 lines missing]" {
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
	want := fmt.Sprintf("[logs cut off: the log stream ended without the runner's confirmation after line %s]", groupDigits(r.Lines))
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

func (f *archiveFixture) appendAttempt(t *testing.T, run, node string, ordinal int, stream string, seq int64) {
	t.Helper()
	code, body := f.send(t, http.MethodPost, "/api/v1/logs/"+run+"/"+node, "Bearer a", fmt.Sprintf("attempt %d line %d\n", ordinal, seq), map[string]string{
		"X-Sparkwing-Claim-Holder":     "holder",
		"X-Sparkwing-Claim-Generation": "1",
		"X-Sparkwing-Attempt-Ordinal":  fmt.Sprint(ordinal),
		LogStreamHeader:                stream,
		LogSeqHeader:                   fmt.Sprint(seq),
	})
	if code != http.StatusNoContent {
		t.Fatalf("append = %d %s", code, body)
	}
}

func (f *archiveFixture) sealAttempt(t *testing.T, run, node string, ordinal int, seal Seal) {
	t.Helper()
	raw, _ := json.Marshal(seal)
	code, body := f.send(t, http.MethodPost, "/api/v1/logs/"+run+"/"+node+"/seal", "Bearer a", string(raw), map[string]string{
		"X-Sparkwing-Claim-Holder":     "holder",
		"X-Sparkwing-Claim-Generation": "1",
		"X-Sparkwing-Attempt-Ordinal":  fmt.Sprint(ordinal),
	})
	if code != http.StatusNoContent {
		t.Fatalf("seal = %d %s", code, body)
	}
}

func TestVerdictJudgesTheLatestAttempt(t *testing.T) {
	f := newArchiveFixture(t, 0)
	// Attempt 1 is cut off; its retry seals cleanly.
	f.appendAttempt(t, "run-a", "build", 1, "first", 1)
	f.appendAttempt(t, "run-a", "build", 1, "first", 2)
	f.appendAttempt(t, "run-a", "build", 2, "retry", 1)
	f.appendAttempt(t, "run-a", "build", 2, "retry", 2)
	f.sealAttempt(t, "run-a", "build", 2, Seal{Stream: "retry", FinalSeq: 2, Lines: 2})
	if got := f.report(t, "Bearer a", "run-a", "build").Assess(done, late); got.State != StateComplete || got.Lines != 4 {
		t.Fatalf("clean retry after a cut-off attempt = %+v, want complete", got)
	}

	// Negative control: a clean first attempt does not excuse a cut-off retry.
	f.appendAttempt(t, "run-b", "build", 1, "first", 1)
	f.sealAttempt(t, "run-b", "build", 1, Seal{Stream: "first", FinalSeq: 1, Lines: 1})
	f.appendAttempt(t, "run-b", "build", 2, "retry", 1)
	if got := f.report(t, "Bearer a", "run-b", "build").Assess(done, late); got.State != StateCutOff {
		t.Fatalf("cut-off retry after a clean attempt = %+v, want cut_off", got)
	}
}
