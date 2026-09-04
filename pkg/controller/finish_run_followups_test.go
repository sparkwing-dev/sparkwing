package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const followUpNodeCount = 60

func seedFinishRunFollowUpState(t *testing.T, st *store.Store, runID string) string {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: runID, Pipeline: "pr-gate", TriggerSource: "github",
		Repo: "acme/sample-app", GithubOwner: "acme", GithubRepo: "sample-app",
		GitSHA: strings.Repeat("1", 40), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "pr-gate", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	last := ""
	for i := range followUpNodeCount {
		nodeID := fmt.Sprintf("node-%02d", i)
		last = nodeID
		if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "success"}); err != nil {
			t.Fatalf("CreateNode %s: %v", nodeID, err)
		}
		for s := range 5 {
			if err := st.AddNodeMetricSample(ctx, runID, nodeID, store.MetricSample{
				TS:            time.Now().UTC().Add(time.Duration(s) * time.Second),
				CPUMillicores: 500,
				MemoryBytes:   1 << 20,
			}); err != nil {
				t.Fatalf("AddNodeMetricSample %s: %v", nodeID, err)
			}
		}
	}
	return last
}

// The terminal run row is already committed when the follow-ups run, and
// nothing else produces a finished run's commit status, so a client that
// disconnects mid-handler must not take the fold or the status with it.
func TestFinishRun_FollowUpsSurviveARequestCancel(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	const runID = "run-disconnect"
	lastNode := seedFinishRunFollowUpState(t, st, runID)

	logs := &lockedBuffer{}
	s := New(st, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer github.Close()
	s.githubCommitStatuses = newGitHubCommitStatusReporter(
		"github-token", "https://sparkwing.example.com", github.URL, github.Client())
	defer s.githubCommitStatuses.stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := make(chan struct{})
	go func() {
		defer close(cancelled)
		for {
			run, err := st.GetRun(context.Background(), runID)
			if err == nil && run != nil && run.FinishedAt != nil {
				cancel()
				return
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()

	body, _ := json.Marshal(finishRunReq{Status: "success"})
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/runs/"+runID+"/finish", strings.NewReader(string(body))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", runID)
	rec := httptest.NewRecorder()
	s.handleFinishRun(rec, req)
	<-cancelled

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s, want 204", rec.Code, rec.Body.String())
	}
	if ctx.Err() == nil {
		t.Fatal("the request context was never cancelled, so the test proved nothing")
	}
	if got := logs.String(); strings.Contains(got, "github commit status trigger lookup failed") {
		t.Errorf("the commit status was skipped after the client went away: %s", got)
	}
	prof, err := st.GetPipelineProfile(context.Background(), "pr-gate", lastNode)
	if err != nil {
		t.Fatalf("GetPipelineProfile: %v", err)
	}
	if prof == nil {
		t.Errorf("the profile fold stopped before %s when the client went away", lastNode)
	}
}

func queuedCommitStatusServer(t *testing.T, apiBase string, httpClient *http.Client, logs *lockedBuffer) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	s := New(st, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	s.githubCommitStatuses = newGitHubCommitStatusReporter("github-token", "", apiBase, httpClient)
	t.Cleanup(s.githubCommitStatuses.stop)
	s.githubCommitStatuses.enqueue(s.logger, githubCommitStatus{
		Owner: "acme", Repo: "sample-app", SHA: strings.Repeat("1", 40),
		Pipeline: "pr-gate", RunID: "run-drain", State: "pending",
		Description: "Sparkwing pipeline is running",
	})
	return s
}

// The status queue holds work a finished run has no other producer for, so
// its drain carries a budget of its own; ServeWith used to hand it the one
// the listener's own shutdown had already spent.
func TestDrainGitHubCommitStatuses_DoesNotInheritASpentBudget(t *testing.T) {
	if finishRunFollowUpTimeout >= controllerShutdownBudget {
		t.Fatalf("finishRunFollowUpTimeout %s must stay under the shutdown budget %s",
			finishRunFollowUpTimeout, controllerShutdownBudget)
	}

	posted := make(chan struct{}, 4)
	release := make(chan struct{})
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		posted <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer github.Close()

	spentLogs := &lockedBuffer{}
	spent := queuedCommitStatusServer(t, github.URL, github.Client(), spentLogs)
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if err := spent.Shutdown(expired); err == nil {
		t.Error("draining on an already-spent budget reported a clean drain")
	}

	liveLogs := &lockedBuffer{}
	live := queuedCommitStatusServer(t, github.URL, github.Client(), liveLogs)
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(release)
	}()
	live.drainGitHubCommitStatuses()
	if got := liveLogs.String(); strings.Contains(got, "github commit status shutdown incomplete") {
		t.Errorf("the drain ran out of budget: %s", got)
	}
	select {
	case <-posted:
	case <-time.After(2 * time.Second):
		t.Error("the queued commit status was never posted")
	}
}
