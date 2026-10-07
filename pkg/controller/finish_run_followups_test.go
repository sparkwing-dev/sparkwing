package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

const followUpNodeCount = 60

func seedFinishRunFollowUpState(t *testing.T, st *store.Store, runID string) string {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: runID, Pipeline: "pr-gate", TriggerSource: "github",
		Repo: "example/project", GithubOwner: "example", GithubRepo: "project", GithubRepoID: 701,
		TriggerEnv: map[string]string{envGitHubAppInstallation: "7"},
		GitSHA:     strings.Repeat("1", 40), CreatedAt: time.Now().UTC(),
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
		if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
			t.Fatalf("CreateNode %s: %v", nodeID, err)
		}
		if err := st.FinishNode(ctx, runID, nodeID, "success", "", nil); err != nil {
			t.Fatal(err)
		}
		for s := range 5 {
			if err := st.AddNodeMetricSample(ctx, runID, nodeID, store.MetricSample{
				Kind:          store.MetricInterval,
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
// nothing else produces a finished run's check run, so a client that
// disconnects mid-handler must not take the fold or the check run with it.
func TestFinishRun_FollowUpsSurviveARequestCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	const runID = "run-disconnect"
	lastNode := seedFinishRunFollowUpState(t, st, runID)

	s := New(st, nil)
	github := attachAppChecks(t, s, "")

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
	drainChecks(t, s)
	if calls := github.CheckRunCalls(); len(calls) != 1 || calls[0].Conclusion != "success" {
		t.Errorf("check run writes = %+v after the client went away, want the completed success", calls)
	}
	prof, err := st.GetPipelineProfile(context.Background(), "pr-gate", lastNode)
	if err != nil {
		t.Fatalf("GetPipelineProfile: %v", err)
	}
	if prof == nil {
		t.Errorf("the profile fold stopped before %s when the client went away", lastNode)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// safety: apiURL replaces the fake's API when set, so a test can hold
// GitHub's answer to the reporter's first request.
func attachAppChecks(t *testing.T, s *Server, apiURL string) *githubapptest.GitHub {
	t.Helper()
	github := githubapptest.New(t)
	account := githubapp.Account{ID: 9, Login: "example", Type: "Organization"}
	github.AddInstallation(githubapptest.Installation{
		ID: 7, Account: account,
		Repos: []githubapptest.Repo{{ID: 701, FullName: "example/project", DefaultBranch: "main"}},
	})
	tenant, err := s.store.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.BindGitHubAppInstallation(t.Context(), store.GitHubAppInstallation{
		InstallationID: 7, AccountID: account.ID, AccountLogin: account.Login, AccountType: account.Type,
	}, time.Now()); err != nil {
		t.Fatalf("bind installation: %v", err)
	}
	cfg := github.Config()
	if apiURL != "" {
		cfg.APIURL = apiURL
	}
	s.WithGitHubApp(cfg)
	t.Cleanup(s.githubApp.checks.stop)
	return github
}

func drainChecks(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := s.githubApp.checks.idle(ctx); err != nil {
		t.Fatalf("drain check runs: %v", err)
	}
}

func queuedCheckServer(t *testing.T, apiURL string, logs *lockedBuffer) *Server {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	s := New(st, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	attachAppChecks(t, s, apiURL)
	s.githubApp.checks.enqueue(s.logger, githubCheckUpdate{
		installation: 7, owner: "example", repo: "project", sha: strings.Repeat("1", 40),
		pipeline: "pr-gate", runID: "run-drain", runStatus: "pending", team: store.DefaultTeam,
	})
	return s
}

// The check run queue holds work a finished run has no other producer for,
// so its drain carries a budget of its own; ServeWith used to hand it the one
// the listener's own shutdown had already spent.
func TestDrainGitHubChecks_DoesNotInheritASpentBudget(t *testing.T) {
	if finishRunFollowUpTimeout >= controllerShutdownBudget {
		t.Fatalf("finishRunFollowUpTimeout %s must stay under the shutdown budget %s",
			finishRunFollowUpTimeout, controllerShutdownBudget)
	}

	// safety: the spent server asks a github of its own, so the gate below
	// can only be opened by the live server's own request.
	spentGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer spentGitHub.Close()

	answered := make(chan struct{}, 4)
	arrived := make(chan struct{}, 4)
	release := make(chan struct{})
	liveGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arrived <- struct{}{}
		<-release
		answered <- struct{}{}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer liveGitHub.Close()

	spent := queuedCheckServer(t, spentGitHub.URL, &lockedBuffer{})
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if err := spent.Shutdown(expired); err == nil {
		t.Error("draining on an already-spent budget reported a clean drain")
	}

	liveLogs := &lockedBuffer{}
	live := queuedCheckServer(t, liveGitHub.URL, liveLogs)
	// safety: the request is in flight when the gate opens, so the drain has
	// to wait for it rather than finding the queue already empty.
	go func() {
		<-arrived
		close(release)
	}()
	live.drainGitHubChecks(t.Context())
	if got := liveLogs.String(); strings.Contains(got, "github check run shutdown incomplete") {
		t.Errorf("the drain ran out of budget: %s", got)
	}
	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		t.Error("the queued check run update never reached GitHub")
	}
}
