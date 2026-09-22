package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the fake answers every bearer with the principal the token
// itself names, so a test addresses two principals without a controller.
func whoamiFor(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// safety: the service also asks the controller whether the writer
		// holds the node's claim, so the fake answers that route too.
		if strings.HasSuffix(r.URL.Path, "/claim/validate") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		name := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"principal":%q,"kind":"user","scopes":["logs.read","logs.write"],"token_prefix":%q}`,
			name, name)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newEgressLogsServer(t *testing.T, cfg egress.Config) (*Server, http.Handler) {
	t.Helper()
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.WithControllerAuth(whoamiFor(t).URL, 0).WithEgressMeter(egress.New(cfg))
	return s, s.Handler()
}

func appendLog(t *testing.T, h http.Handler, token, runID, nodeID, text string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs/"+runID+"/"+nodeID, strings.NewReader(text))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code >= 300 {
		t.Fatalf("append = %d, body %s", rec.Code, rec.Body.String())
	}
}

func readLog(t *testing.T, h http.Handler, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLogReadsCountAgainstThePrincipalsBudget(t *testing.T) {
	// safety: the budget is exactly one read of the log, because a read
	// that would pass it is cut and aborted rather than finished.
	s, h := newEgressLogsServer(t, egress.Config{PerPrincipalMonthlyBytes: 31})
	appendLog(t, h, "alice", "r1", "n1", strings.Repeat("x", 30)+"\n")

	first := readLog(t, h, "alice", "/api/v1/logs/r1/n1")
	if first.Code != http.StatusOK {
		t.Fatalf("first read = %d, body %s", first.Code, first.Body.String())
	}
	if state := s.egress.State(); state.GlobalMonthBytes < 30 {
		t.Fatalf("metered %d bytes, want at least the log body", state.GlobalMonthBytes)
	}

	refused := readLog(t, h, "alice", "/api/v1/logs/r1/n1")
	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("second read = %d, want 429", refused.Code)
	}
	retry, err := strconv.Atoi(refused.Header().Get("Retry-After"))
	if err != nil || retry <= 0 {
		t.Fatalf("Retry-After = %q, want a positive number of seconds", refused.Header().Get("Retry-After"))
	}
	var body EgressRefusalBody
	if err := json.Unmarshal(refused.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refusal: %v -- raw %q", err, refused.Body.String())
	}
	if body.Code != EgressBudgetCode || body.Principal != "alice" {
		t.Fatalf("refusal = %+v", body)
	}
	if !strings.Contains(body.Error, "egress budget exceeded") {
		t.Errorf("error member %q does not carry the reason", body.Error)
	}

	// safety: one tenant over budget must not refuse another's reads.
	other := readLog(t, h, "bob", "/api/v1/logs/r1/n1")
	if other.Code != http.StatusOK {
		t.Fatalf("another principal's read = %d, want 200", other.Code)
	}
}

func TestRunLogReadAndSearchAreMetered(t *testing.T) {
	s, h := newEgressLogsServer(t, egress.Config{})
	appendLog(t, h, "alice", "r1", "n1", "hello world\n")

	before := s.egress.State().GlobalMonthBytes
	if run := readLog(t, h, "alice", "/api/v1/logs/r1"); run.Code != http.StatusOK {
		t.Fatalf("run read = %d, body %s", run.Code, run.Body.String())
	}
	afterRun := s.egress.State().GlobalMonthBytes
	if afterRun <= before {
		t.Fatalf("a run read metered %d bytes, want more than %d", afterRun, before)
	}

	if search := readLog(t, h, "alice", "/api/v1/logs/search?q=hello&run_id=r1"); search.Code != http.StatusOK {
		t.Fatalf("search = %d, body %s", search.Code, search.Body.String())
	}
	if s.egress.State().GlobalMonthBytes <= afterRun {
		t.Fatal("a search metered no bytes")
	}
}

func TestLogReadsWithoutABearerAreRefusedAndMeterNothing(t *testing.T) {
	s, h := newEgressLogsServer(t, egress.Config{})
	appendLog(t, h, "alice", "r1", "n1", "hello\n")

	for _, path := range []string{
		"/api/v1/logs/r1/n1",
		"/api/v1/logs/r1",
		"/api/v1/logs/r1/n1/stream",
		"/api/v1/logs/search?q=hello&run_id=r1",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a bearer = %d, want 401", path, rec.Code)
		}
	}
	if state := s.egress.State(); state.GlobalMonthBytes != 0 {
		t.Errorf("an unauthenticated refusal metered %d bytes, want 0", state.GlobalMonthBytes)
	}
}

func TestLiveLogStreamCapRefusesPastTheLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	s, h := newEgressLogsServer(t, egress.Config{MaxStreamsPerPrincipal: 1})
	appendLog(t, h, "alice", "r1", "n1", "hello\n")

	release, err := s.egress.Open("alice", egress.SlotLogStream)
	if err != nil {
		t.Fatalf("reserve the only slot: %v", err)
	}
	defer release()

	refused := readLog(t, h, "alice", "/api/v1/logs/r1/n1/stream")
	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("stream past the cap = %d, want 429", refused.Code)
	}
	var body EgressRefusalBody
	if err := json.Unmarshal(refused.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refusal: %v -- raw %q", err, refused.Body.String())
	}
	if body.Code != EgressStreamLimitCode {
		t.Fatalf("refusal code = %q, want %q", body.Code, EgressStreamLimitCode)
	}
	if !strings.Contains(body.Error, "egress concurrency limit reached") {
		t.Errorf("error member %q does not carry the reason", body.Error)
	}
	if refused.Header().Get("Retry-After") == "" {
		t.Error("the stream refusal named no Retry-After")
	}

	// safety: another principal has its own slots.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs/r1/n1/stream", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer bob")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusTooManyRequests {
		t.Error("another principal was refused by the first principal's stream cap")
	}
}

func TestLogsHealthReportsTheEgressAlarm(t *testing.T) {
	s, h := newEgressLogsServer(t, egress.Config{GlobalDailyAlarmBytes: 10})

	var health struct {
		Status   string         `json:"status"`
		Problems []string       `json:"problems"`
		Egress   map[string]any `json:"egress"`
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode health: %v -- raw %q", err, rec.Body.String())
	}
	if health.Egress["alarm"] != false {
		t.Fatalf("health egress = %+v, want no alarm", health.Egress)
	}

	appendLog(t, h, "alice", "r1", "n1", strings.Repeat("x", 50)+"\n")
	if read := readLog(t, h, "alice", "/api/v1/logs/r1/n1"); read.Code != http.StatusOK {
		t.Fatalf("read = %d", read.Code)
	}
	if !s.egress.Alarm() {
		t.Fatal("the alarm did not rise past the daily threshold")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode health: %v -- raw %q", err, rec.Body.String())
	}
	if health.Egress["alarm"] != true || health.Status != "degraded" {
		t.Fatalf("health = %+v, want a degraded status with the alarm up", health)
	}
	var named bool
	for _, p := range health.Problems {
		named = named || strings.Contains(p, "egress")
	}
	if !named {
		t.Errorf("health problems = %v, want one naming egress", health.Problems)
	}
}

func TestLogsServerWithoutAMeterServesUnmetered(t *testing.T) {
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := s.Handler()
	appendLog(t, h, "", "r1", "n1", "hello\n")
	if read := readLog(t, h, "", "/api/v1/logs/r1/n1"); read.Code != http.StatusOK {
		t.Fatalf("read with no meter = %d, body %s", read.Code, read.Body.String())
	}
	state, problems := s.egressHealth()
	if state["enabled"] != false || len(problems) != 0 {
		t.Fatalf("health without a meter = %+v, %v", state, problems)
	}
}

func TestHeadReadsChargeNothing(t *testing.T) {
	s, h := newEgressLogsServer(t, egress.Config{GlobalDailyAlarmBytes: 1})
	appendLog(t, h, "alice", "r1", "n1", strings.Repeat("x", 4096)+"\n")

	for range 8 {
		req := httptest.NewRequest(http.MethodHead, "/api/v1/logs/r1/n1", nil)
		req.Header.Set("Authorization", "Bearer alice")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("HEAD = %d, body %s", rec.Code, rec.Body.String())
		}
	}
	// safety: a recorder keeps a HEAD body that net/http would discard, so
	// the meter must decide on the method rather than on what was written.
	if state := s.egress.State(); state.GlobalDayBytes != 0 {
		t.Fatalf("eight HEADs charged %d bytes, want 0", state.GlobalDayBytes)
	}
	if s.egress.Alarm() {
		t.Error("a HEAD raised the daily alarm")
	}
}

// safety: an error body is not the download the budget is for, so a
// refusal must not spend a principal's month.
func TestARefusedReadIsNotCharged(t *testing.T) {
	s, h := newEgressLogsServer(t, egress.Config{})
	appendLog(t, h, "alice", "r1", "n1", "hello\n")

	rec := readLog(t, h, "alice", "/api/v1/logs/r1/n1?tail=notanumber")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a malformed filter = %d, want 400", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("the refusal carried no body, so the test proves nothing")
	}
	if state := s.egress.State(); state.GlobalDayBytes != 0 {
		t.Fatalf("a refused read charged %d bytes, want 0", state.GlobalDayBytes)
	}
}

// safety: the runner identity is a header the caller writes, so a slot
// keyed on it let one bearer hold as many streams as it named pods. This
// drives logs.Client itself, the way a pool's pods reach the service.
func TestPodsUnderOneBearerShareItsStreamSlots(t *testing.T) {
	_, h := newEgressLogsServer(t, egress.Config{MaxStreamsPerPrincipal: 1})
	appendLog(t, h, "alice", "r1", "n1", "hello\n")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	pod := func(name string) *Client {
		return NewClientWithToken(srv.URL, nil, "alice").WithRunnerIdentity(name)
	}
	body, err := pod("pool-runner-0").Stream(t.Context(), "r1", "n1")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	t.Cleanup(func() { _ = body.Close() })

	if second, err := pod("pool-runner-1").Stream(t.Context(), "r1", "n1"); err == nil {
		_ = second.Close()
		t.Fatal("a second pod under the same bearer was admitted past the principal's cap of one")
	}
}

func TestReadPathsCarryTheRunnerIdentity(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+" "+r.Header.Get(store.RunnerIdentityHeader))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hi\n\n"))
	}))
	t.Cleanup(srv.Close)

	cli := NewClient(srv.URL, nil).WithRunnerIdentity("pod-7")
	if _, err := cli.ReadRun(t.Context(), "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.ReadFiltered(t.Context(), "r1", "n1", ReadFilter{Tail: 5}); err != nil {
		t.Fatal(err)
	}
	body, err := cli.Stream(t.Context(), "r1", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Fatalf("saw %d requests, want the three metered read paths", len(seen))
	}
	for _, got := range seen {
		if !strings.HasSuffix(got, " pod-7") {
			t.Errorf("request %q carried no runner identity", got)
		}
	}
}
