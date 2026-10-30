package controller_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type floodFixture struct{ *browserFixture }

func newFloodServer(t *testing.T, policy controller.FloodPolicy, opts ...store.Option) *floodFixture {
	t.Helper()
	f := newBrowserFixture(t, nil, opts...)
	f.srv.WithFloodPolicy(policy)
	f.h = f.srv.Handler()
	return &floodFixture{f}
}

func fortyHex(seed int) string { return fmt.Sprintf("%040x", seed) }

func (f *floodFixture) submit(token, pipeline, sha string) *httptest.ResponseRecorder {
	f.t.Helper()
	body := computeIntake(pipeline)
	body.Request.Git.SHA, body.Request.Source.Ref = sha, sha
	return f.intake(token, body)
}

func (f *floodFixture) intake(token string, body controllerwire.CreateRunRequest) *httptest.ResponseRecorder {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return f.serve(req)
}

func floodRun(t *testing.T, response *httptest.ResponseRecorder, status int) controllerwire.CreateRunResponse {
	t.Helper()
	var out controllerwire.CreateRunResponse
	if response.Code != status || json.Unmarshal(response.Body.Bytes(), &out) != nil || out.RunID == "" {
		t.Fatalf("intake = %d %s, want %d and a run", response.Code, response.Body, status)
	}
	return out
}

func TestFloodPolicy_CapsRunsPerPrincipalHour(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{RunsPerPrincipalHour: 2})
	for i := range 2 {
		floodRun(t, f.submit(f.token, "build", fortyHex(i)), 201)
	}
	refused := f.submit(f.token, "build", fortyHex(9))
	if refused.Code != 429 {
		t.Fatalf("cap = %d %s", refused.Code, refused.Body)
	}
	for _, want := range []string{"limit is 2 per hour", "usage is at least 2", "ask the operator"} {
		if !strings.Contains(refused.Body.String(), want) {
			t.Fatalf("hourly refusal omits %q: %s", want, refused.Body.String())
		}
	}
	if seconds, err := strconv.Atoi(refused.Header().Get("Retry-After")); err != nil || seconds <= 0 {
		t.Fatalf("Retry-After = %q", refused.Header().Get("Retry-After"))
	}
}

func TestFloodPolicy_UnsetCapAdmitsEverySubmission(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	for i := range 5 {
		floodRun(t, f.submit(f.token, "build", fortyHex(i)), 201)
	}
}

func TestFloodPolicy_ShedsAboveTheQueueDepthThreshold(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	for i := range 2 {
		floodRun(t, f.submit(f.token, "build", fortyHex(i)), 201)
	}
	f.srv.WithFloodPolicy(controller.FloodPolicy{ShedQueueDepth: 2})
	refused := f.submit(f.token, "build", fortyHex(3))
	if refused.Code != 503 || refused.Header().Get("Retry-After") == "" {
		t.Fatalf("shed = %d %s headers %v", refused.Code, refused.Body, refused.Header())
	}
	for _, want := range []string{"limit is 2", "current usage is 2", "ask the operator"} {
		if !strings.Contains(refused.Body.String(), want) {
			t.Fatalf("queue refusal omits %q: %s", want, refused.Body.String())
		}
	}
}

func TestFloodPolicyIdenticalSubmissionsWithoutKeysCreateSeparateRuns(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	first := floodRun(t, f.submit(f.token, "build", fortyHex(1)), 201)
	second := floodRun(t, f.submit(f.token, "build", fortyHex(1)), 201)
	if first.RunID == second.RunID {
		t.Fatal("identical submissions without keys reused a run")
	}
}

func TestFloodPolicyConcurrentExplicitKeyCommitsOneRun(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 8)
	var wg sync.WaitGroup
	for range cap(responses) {
		wg.Go(func() {
			<-start
			body := computeIntake("build")
			body.IdempotencyKey = "concurrent-intake"
			responses <- f.intake(f.token, body)
		})
	}
	close(start)
	wg.Wait()
	close(responses)
	created, id := 0, ""
	for response := range responses {
		if response.Code == 201 {
			created++
		}
		var out controllerwire.CreateRunResponse
		if (response.Code != 200 && response.Code != 201) || json.Unmarshal(response.Body.Bytes(), &out) != nil || out.RunID == "" {
			t.Fatalf("concurrent intake = %d %s", response.Code, response.Body)
		}
		if id != "" && out.RunID != id {
			t.Fatalf("runs = %s and %s", id, out.RunID)
		}
		id = out.RunID
	}
	if created != 1 {
		t.Fatalf("created = %d", created)
	}
	var count int
	if err := teststore.SQL(t, f.st).QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stored runs = %d: %v", count, err)
	}
}

func TestFloodPolicyExplicitIntakeKeysIdentifyRuns(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	body := computeIntake("build")
	body.IdempotencyKey = "one"
	first := floodRun(t, f.intake(f.token, body), 201)
	body.IdempotencyKey = "two"
	second := floodRun(t, f.intake(f.token, body), 201)
	if first.RunID == second.RunID {
		t.Fatal("explicit invocation keys were collapsed")
	}
	body.IdempotencyKey = "one"
	replay := floodRun(t, f.intake(f.token, body), 200)
	if replay.RunID != first.RunID || !replay.Replayed {
		t.Fatalf("explicit replay = %+v", replay)
	}
}

func TestFloodPolicyExplicitReplayRechecksAuthorityAndReturnsHereBearerOnce(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	body := computeIntake("build")
	body.IdempotencyKey = "here-replay"
	body.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	first := floodRun(t, f.intake(f.token, body), 201)
	if first.HereBearer == "" {
		t.Fatal("initial here authority is missing")
	}
	replay := floodRun(t, f.intake(f.token, body), 200)
	if replay.RunID != first.RunID || !replay.Replayed || replay.HereBearer != "" {
		t.Fatalf("replay = run %s, replayed %v, bearer present %v", replay.RunID, replay.Replayed, replay.HereBearer != "")
	}
	principal, err := f.st.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(f.token)))
	if err != nil {
		t.Fatal(err)
	}
	if response := f.serve(f.cookieRequest("DELETE", "/api/v1/tokens/"+principal.Capability.Prefix, "")); response.Code != 204 {
		t.Fatalf("revoke = %d %s", response.Code, response.Body)
	}
	if response := f.intake(f.token, body); response.Code != 401 {
		t.Fatalf("revoked replay = %d %s", response.Code, response.Body)
	}
}

func TestFloodPolicyReplaysNeverConsumeOrExhaustTheBucket(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{RunsPerPrincipalHour: 1})
	body := computeIntake("build")
	body.IdempotencyKey = "replay"
	first := floodRun(t, f.intake(f.token, body), 201)
	replay := floodRun(t, f.intake(f.token, body), 200)
	if first.RunID != replay.RunID {
		t.Fatal("replay changed run")
	}
	if got := f.submit(f.token, "other", fortyHex(7)); got.Code != 429 {
		t.Fatalf("new run bypassed cap: %d %s", got.Code, got.Body)
	}
	floodRun(t, f.intake(f.token, body), 200)
}

func TestFloodPolicyExplicitKeyRetryAfterStoreRefusal(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	body := computeIntake("build")
	body.IdempotencyKey = "retry-store-refusal"
	if _, err := teststore.SQL(t, f.st).Exec(`CREATE TRIGGER refuse_intake BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT,'intake refused'); END`); err != nil {
		t.Fatal(err)
	}
	if got := f.intake(f.token, body); got.Code != 500 {
		t.Fatalf("store refusal: %d %s", got.Code, got.Body)
	}
	if _, err := teststore.SQL(t, f.st).Exec(`DROP TRIGGER refuse_intake`); err != nil {
		t.Fatal(err)
	}
	first := floodRun(t, f.intake(f.token, body), 201)
	replay := floodRun(t, f.intake(f.token, body), 200)
	if first.RunID != replay.RunID {
		t.Fatal("retry did not establish replay")
	}
}

func TestFloodPolicyRetryQueueRefusalNamesLimitUsageAndRemedy(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	first := floodRun(t, f.submit(f.token, "build", fortyHex(1)), 201)
	if err := teststore.SeedRunFinish(f.st, t.Context(), store.DefaultTeam, first.RunID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		floodRun(t, f.submit(f.token, "build", fortyHex(i+2)), 201)
	}
	f.srv.WithFloodPolicy(controller.FloodPolicy{ShedQueueDepth: 2})
	body := strings.NewReader(`{"mode":"all","idempotency_key":"queue-refused"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+first.RunID+"/retry", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token)
	refused := f.serve(req)
	if refused.Code != 503 || refused.Header().Get("Retry-After") == "" {
		t.Fatalf("retry shed: %d %s", refused.Code, refused.Body)
	}
	for _, want := range []string{"limit is 2", "current usage is 3", "ask the operator"} {
		if !strings.Contains(refused.Body.String(), want) {
			t.Fatalf("retry refusal omits %q: %s", want, refused.Body.String())
		}
	}
}
