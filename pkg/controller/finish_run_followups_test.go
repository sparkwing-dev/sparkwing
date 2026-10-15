package controller

import (
	"bytes"
	"context"
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
