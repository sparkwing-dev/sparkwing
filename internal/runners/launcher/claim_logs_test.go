package launcher_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (f launchFixture) claimToken(t *testing.T, runID, nodeID string) string {
	t.Helper()
	req := store.LaunchClaimRequest{HolderID: "launcher:test", Lease: time.Minute, Deadline: time.Hour, RunID: runID, NodeID: nodeID}
	c, err := f.st.ClaimLaunch(store.WithoutCreditMetering(context.Background()),
		store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"}, req, time.Now())
	if err != nil || c == nil {
		t.Fatalf("claim %s/%s: %+v %v", runID, nodeID, c, err)
	}
	return c.Token
}

// A work claim token writes its own node's durable log, and only that: not
// another node's, not through a planning claim, not once its claim ended,
// and it reads and deletes nothing.
func TestLogs_AClaimTokenWritesOnlyItsOwnNodesLog(t *testing.T) {
	ctx := context.Background()
	f := newLaunchFixture(t)
	f.optedInRun(t, "run-logs")
	plan := f.claimToken(t, "run-logs", store.PlanNodeID)
	hash := "sha256:" + strings.Repeat("a", 64)
	doc := `{"nodes":[{"id":"a","deps":[],"spec_hash":"` + hash + `"},{"id":"b","deps":[],"spec_hash":"` + hash + `"}]}`
	if err := client.NewWithToken(f.url, nil, plan).SubmitPlan(ctx, "run-logs", []byte(doc)); err != nil {
		t.Fatal(err)
	}
	a, b := f.claimToken(t, "run-logs", "a"), f.claimToken(t, "run-logs", "b")
	srv, err := logs.New(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.WithControllerAuth(f.url, time.Minute).Handler())
	t.Cleanup(ts.Close)
	call := func(method, path, token string) int {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader("{\"msg\":\"line\"}\n"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := call("POST", "/api/v1/logs/run-logs/a", a); code != http.StatusNoContent {
		t.Fatalf("own node's append = %d, want 204", code)
	}
	for _, c := range []struct {
		method, path, token, what string
	}{
		{"POST", "/api/v1/logs/run-logs/b", a, "another node's append"},
		{"POST", "/api/v1/logs/run-logs/plan", plan, "a planning claim's append"},
		{"GET", "/api/v1/logs/run-logs/a", a, "a read"},
		{"DELETE", "/api/v1/logs/run-logs", a, "a delete"},
	} {
		if code := call(c.method, c.path, c.token); code < 400 || code >= 500 {
			t.Errorf("%s = %d, want refused", c.what, code)
		}
	}
	if err := client.NewWithToken(f.url, nil, b).ReportAttempt(ctx, "run-logs", "b", store.AttemptReport{Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	if code := call("POST", "/api/v1/logs/run-logs/b", b); code < 400 || code >= 500 {
		t.Fatalf("an append after the claim ended = %d, want refused", code)
	}
}
