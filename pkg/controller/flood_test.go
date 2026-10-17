package controller_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

type floodFixture struct{ *browserFixture }

func newFloodServer(t *testing.T, policy controller.FloodPolicy) *floodFixture {
	t.Helper()
	f := newBrowserFixture(t, nil)
	f.srv.WithFloodPolicy(policy)
	f.h = f.srv.Handler()
	return &floodFixture{f}
}

func fortyHex(seed int) string { return fmt.Sprintf("%040x", seed) }

func (f *floodFixture) submit(token, pipeline, sha string) *httptest.ResponseRecorder {
	f.t.Helper()
	body := computeIntake(pipeline)
	body.Request.Git.SHA = sha
	return f.intake(token, body)
}

func (f *floodFixture) intake(token string, body controllerwire.CreateRunRequest) *httptest.ResponseRecorder {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/runs", bytes.NewReader(raw))
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
}

func TestFloodPolicy_DedupesIdenticalSubmissionsInsideTheWindow(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{DedupeWindow: time.Minute})
	first := floodRun(t, f.submit(f.token, "build", fortyHex(1)), 201)
	repeat := floodRun(t, f.submit(f.token, "build", fortyHex(1)), 200)
	if repeat.RunID != first.RunID || !repeat.Replayed {
		t.Fatalf("repeat = %+v, first = %+v", repeat, first)
	}
	different := floodRun(t, f.submit(f.token, "build", fortyHex(2)), 201)
	if different.RunID == first.RunID {
		t.Fatal("different source reused a run")
	}
}

func TestFloodPolicy_UnsetWindowDedupesNothing(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	first := floodRun(t, f.submit(f.token, "build", fortyHex(1)), 201)
	second := floodRun(t, f.submit(f.token, "build", fortyHex(1)), 201)
	if first.RunID == second.RunID {
		t.Fatal("unset window reused a run")
	}
}

func TestFloodPolicy_DedupeIsScopedToThePrincipal(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{DedupeWindow: time.Minute})
	other := floodMember(t, f, "default", "other")
	first := floodRun(t, f.submit(f.token, "build", fortyHex(7)), 201)
	second := floodRun(t, f.submit(other, "build", fortyHex(7)), 201)
	if first.RunID == second.RunID {
		t.Fatal("another principal received the first principal's run")
	}
	repeat := floodRun(t, f.submit(f.token, "build", fortyHex(7)), 200)
	if repeat.RunID != first.RunID || !repeat.Replayed {
		t.Fatalf("repeat = %+v", repeat)
	}
}

func TestFloodPolicyConcurrentIdenticalIntakeCommitsOneRun(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{DedupeWindow: time.Minute})
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 8)
	var wg sync.WaitGroup
	for range cap(responses) {
		wg.Go(func() { <-start; responses <- f.submit(f.token, "build", fortyHex(1)) })
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
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stored runs = %d: %v", count, err)
	}
}

func TestFloodPolicyExplicitIntakeKeysTakePrecedence(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{DedupeWindow: time.Minute})
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

func TestFloodPolicyDedupeRechecksAuthorityAndReturnsHereBearerOnce(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{DedupeWindow: time.Minute})
	body := computeIntake("build")
	body.Request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	first := floodRun(t, f.intake(f.token, body), 201)
	if first.HereBearer == "" {
		t.Fatal("initial here authority is missing")
	}
	replay := floodRun(t, f.intake(f.token, body), 200)
	if replay.RunID != first.RunID || !replay.Replayed || replay.HereBearer != "" {
		t.Fatalf("replay = run %s, replayed %v, bearer present %v", replay.RunID, replay.Replayed, replay.HereBearer != "")
	}
	principal, err := f.st.LookupCapability(t.Context(), sha256.Sum256([]byte(f.token)))
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
	for _, explicit := range []bool{false, true} {
		t.Run(strconv.FormatBool(explicit), func(t *testing.T) {
			f := newFloodServer(t, controller.FloodPolicy{RunsPerPrincipalHour: 1, DedupeWindow: time.Minute})
			body := computeIntake("build")
			if explicit {
				body.IdempotencyKey = "replay"
			}
			first := floodRun(t, f.intake(f.token, body), 201)
			replay := floodRun(t, f.intake(f.token, body), 200)
			if first.RunID != replay.RunID {
				t.Fatal("replay changed run")
			}
			if got := f.submit(f.token, "other", fortyHex(7)); got.Code != 429 {
				t.Fatalf("new run bypassed cap: %d %s", got.Code, got.Body)
			}
			floodRun(t, f.intake(f.token, body), 200)
		})
	}
}

func TestFloodPolicyDedupeRetryAfterStoreRefusal(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{DedupeWindow: time.Minute})
	if _, err := f.st.DB().Exec(`CREATE TRIGGER refuse_intake BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT,'intake refused'); END`); err != nil {
		t.Fatal(err)
	}
	if got := f.submit(f.token, "build", fortyHex(1)); got.Code != 500 {
		t.Fatalf("store refusal: %d %s", got.Code, got.Body)
	}
	if _, err := f.st.DB().Exec(`DROP TRIGGER refuse_intake`); err != nil {
		t.Fatal(err)
	}
	first := floodRun(t, f.submit(f.token, "build", fortyHex(1)), 201)
	replay := floodRun(t, f.submit(f.token, "build", fortyHex(1)), 200)
	if first.RunID != replay.RunID {
		t.Fatal("retry did not establish dedupe")
	}
}
