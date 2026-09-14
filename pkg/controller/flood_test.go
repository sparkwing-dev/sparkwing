package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newFloodServer(t *testing.T, p controller.FloodPolicy) (string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ts := httptest.NewServer(controller.New(st, nil).WithFloodPolicy(p).Handler())
	t.Cleanup(ts.Close)
	return ts.URL, st
}

func submitTrigger(t *testing.T, base, pipeline, sha string) (int, string) {
	t.Helper()
	return postJSONWithStatus(t, base+"/api/v1/triggers", map[string]any{
		"pipeline": pipeline,
		"trigger":  map[string]string{"source": "api", "user": "alice"},
		"git":      map[string]string{"branch": "main", "sha": sha},
	})
}

func jsonBody(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return bytes.NewReader(raw)
}

func fortyHex(seed int) string {
	return fmt.Sprintf("%040x", seed)
}

func TestFloodPolicy_CapsRunsPerPrincipalHour(t *testing.T) {
	base, _ := newFloodServer(t, controller.FloodPolicy{RunsPerPrincipalHour: 2})

	for i := range 2 {
		if status, body := submitTrigger(t, base, "build", fortyHex(i)); status != http.StatusAccepted {
			t.Fatalf("submission %d status=%d want 202 (body %s)", i, status, body)
		}
	}

	resp, err := http.Post(base+"/api/v1/triggers", "application/json",
		jsonBody(t, map[string]any{
			"pipeline": "build",
			"trigger":  map[string]string{"source": "api", "user": "alice"},
			"git":      map[string]string{"branch": "main", "sha": fortyHex(9)},
		}))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("capped submission status=%d want 429", resp.StatusCode)
	}
	after, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || after <= 0 {
		t.Errorf("Retry-After=%q want a positive whole number of seconds", resp.Header.Get("Retry-After"))
	}
}

func TestFloodPolicy_UnsetCapAdmitsEverySubmission(t *testing.T) {
	base, _ := newFloodServer(t, controller.FloodPolicy{})
	for i := range 5 {
		if status, body := submitTrigger(t, base, "build", fortyHex(i)); status != http.StatusAccepted {
			t.Fatalf("submission %d status=%d want 202 (body %s)", i, status, body)
		}
	}
}

func TestFloodPolicy_ShedsAboveTheQueueDepthThreshold(t *testing.T) {
	base, st := newFloodServer(t, controller.FloodPolicy{ShedQueueDepth: 2})
	ctx := context.Background()
	for i := range 2 {
		if err := st.CreateTrigger(ctx, store.Trigger{
			ID: fmt.Sprintf("run-seed-%d", i), Pipeline: "build", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("seed trigger %d: %v", i, err)
		}
	}

	resp, err := http.Post(base+"/api/v1/triggers", "application/json",
		jsonBody(t, map[string]any{
			"pipeline": "build",
			"trigger":  map[string]string{"source": "api", "user": "alice"},
			"git":      map[string]string{"branch": "main", "sha": fortyHex(1)},
		}))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("shed submission status=%d want 503", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("shed submission carried no Retry-After")
	}
}

func TestFloodPolicy_DedupesIdenticalSubmissionsInsideTheWindow(t *testing.T) {
	base, _ := newFloodServer(t, controller.FloodPolicy{DedupeWindow: time.Minute})

	status, body := submitTrigger(t, base, "build", fortyHex(1))
	if status != http.StatusAccepted {
		t.Fatalf("first submission status=%d want 202 (body %s)", status, body)
	}
	var first struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(body), &first); err != nil {
		t.Fatalf("decode first submission: %v", err)
	}

	status, body = submitTrigger(t, base, "build", fortyHex(1))
	if status != http.StatusConflict {
		t.Fatalf("repeat submission status=%d want 409 (body %s)", status, body)
	}
	var repeat struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &repeat); err != nil {
		t.Fatalf("decode repeat submission: %v", err)
	}
	if repeat.Status != "duplicate" || repeat.RunID != first.RunID {
		t.Errorf("repeat answered %+v; want duplicate naming %s", repeat, first.RunID)
	}

	if status, body := submitTrigger(t, base, "build", fortyHex(2)); status != http.StatusAccepted {
		t.Fatalf("differing submission status=%d want 202 (body %s)", status, body)
	}
}

func TestFloodPolicy_UnsetWindowDedupesNothing(t *testing.T) {
	base, _ := newFloodServer(t, controller.FloodPolicy{})
	for i := range 2 {
		if status, body := submitTrigger(t, base, "build", fortyHex(1)); status != http.StatusAccepted {
			t.Fatalf("submission %d status=%d want 202 (body %s)", i, status, body)
		}
	}
}

func TestFloodPolicy_DedupeIsScopedToThePrincipal(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	tenantA, _, err := st.CreateToken("tenant-a", store.TokenKindUser,
		[]string{controller.ScopeRunsWrite}, 0, now)
	if err != nil {
		t.Fatalf("tenant-a token: %v", err)
	}
	tenantB, _, err := st.CreateToken("tenant-b", store.TokenKindUser,
		[]string{controller.ScopeRunsWrite}, 0, now)
	if err != nil {
		t.Fatalf("tenant-b token: %v", err)
	}
	ts := httptest.NewServer(controller.New(st, nil).
		EnableAuthFromStore().
		WithFloodPolicy(controller.FloodPolicy{DedupeWindow: time.Minute}).
		Handler())
	t.Cleanup(ts.Close)

	submission := map[string]any{
		"pipeline": "build",
		"trigger":  map[string]string{"source": "api", "user": "ci"},
		"git":      map[string]string{"branch": "main", "sha": fortyHex(7)},
	}
	status, body := postJSONWithBearer(t, ts.URL+"/api/v1/triggers", tenantA, submission)
	if status != http.StatusAccepted {
		t.Fatalf("tenant-a submission status=%d want 202 (body %s)", status, body)
	}
	var first struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(body), &first); err != nil {
		t.Fatalf("decode tenant-a submission: %v", err)
	}

	status, body = postJSONWithBearer(t, ts.URL+"/api/v1/triggers", tenantB, submission)
	if status != http.StatusAccepted {
		t.Fatalf("tenant-b submission status=%d want 202; one tenant was answered with another's run (body %s)",
			status, body)
	}
	var second struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(body), &second); err != nil {
		t.Fatalf("decode tenant-b submission: %v", err)
	}
	if second.RunID == first.RunID {
		t.Errorf("tenant-b was handed tenant-a's run %s", first.RunID)
	}

	status, _ = postJSONWithBearer(t, ts.URL+"/api/v1/triggers", tenantA, submission)
	if status != http.StatusConflict {
		t.Errorf("tenant-a repeat status=%d want 409; its own redelivery must still dedupe", status)
	}
}

func TestFloodPolicy_GitHubRedeliveryDoesNotSpendTheCap(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ts := httptest.NewServer(controller.New(st, nil).
		WithGitHubWebhookSecret(testWebhookSecret).
		WithFloodPolicy(controller.FloodPolicy{RunsPerPrincipalHour: 2}).
		Handler())
	t.Cleanup(ts.Close)

	push := func(delivery, sha string) *http.Response {
		body := []byte(fmt.Sprintf(`{
			"ref": "refs/heads/main",
			"before": "0000000000000000000000000000000000000000",
			"after": %q,
			"repository": {"full_name": "acme/sample-app"},
			"pusher": {"name": "alice", "email": "alice@example.com"}
		}`, sha))
		return postWebhookDelivery(t, ts.URL+"/webhooks/github/build", "push", delivery,
			body, signWebhook(testWebhookSecret, body))
	}
	for _, step := range []struct {
		delivery string
		sha      string
		want     int
		note     string
	}{
		{"delivery-1", fortyHex(1), http.StatusAccepted, "first delivery"},
		{"delivery-1", fortyHex(1), http.StatusConflict, "redelivery"},
		{"delivery-2", fortyHex(2), http.StatusAccepted, "second delivery; the redelivery burned a token of the cap"},
		{"delivery-3", fortyHex(3), http.StatusTooManyRequests, "third delivery"},
	} {
		resp := push(step.delivery, step.sha)
		status := resp.StatusCode
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close %s: %v", step.note, err)
		}
		if status != step.want {
			t.Fatalf("%s status=%d want %d", step.note, status, step.want)
		}
	}
}

func TestFloodPolicy_CapsGitHubDeliveriesPerRepository(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := controller.New(st, nil).
		WithGitHubWebhookSecret(testWebhookSecret).
		WithFloodPolicy(controller.FloodPolicy{RunsPerPrincipalHour: 1})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	push := func(delivery, sha string) *http.Response {
		body := []byte(fmt.Sprintf(`{
			"ref": "refs/heads/main",
			"before": "0000000000000000000000000000000000000000",
			"after": %q,
			"repository": {"full_name": "acme/sample-app"},
			"pusher": {"name": "alice", "email": "alice@example.com"}
		}`, sha))
		return postWebhookDelivery(t, ts.URL+"/webhooks/github/build", "push", delivery,
			body, signWebhook(testWebhookSecret, body))
	}

	first := push("delivery-1", fortyHex(1))
	defer func() { _ = first.Body.Close() }()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first delivery status=%d want 202", first.StatusCode)
	}

	second := push("delivery-2", fortyHex(2))
	defer func() { _ = second.Body.Close() }()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("capped delivery status=%d want 429", second.StatusCode)
	}
	if second.Header.Get("Retry-After") == "" {
		t.Error("capped delivery carried no Retry-After")
	}
}
