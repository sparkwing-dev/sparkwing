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
	s, h := newEgressLogsServer(t, egress.Config{PerPrincipalMonthlyBytes: 20})
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
	s, h := newEgressLogsServer(t, egress.Config{MaxStreamsPerPrincipal: 1})
	appendLog(t, h, "alice", "r1", "n1", "hello\n")

	release, err := s.egress.OpenStream("alice")
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
