package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// A node whose setup fails on a pooled runner is finished as failed with
// the real error at once, rather than sitting claimed until its lease lapses
// and reporting a lost runner minutes later.
func TestPooledNodeSetupFailureFinishesTheNodeWithTheError(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(srv.Close)
	ctrl := client.New(srv.URL, nil)
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "r1", "n1"); err != nil {
		t.Fatal(err)
	}
	claimed, err := ctrl.ClaimNode(ctx, "pool:1", nil, 3*time.Minute, nil)
	if err != nil || claimed == nil {
		t.Fatalf("claim node: %+v, %v", claimed, err)
	}

	saved := runPooledNodeOnce
	t.Cleanup(func() { runPooledNodeOnce = saved })
	runPooledNodeOnce = func(context.Context, string, string, string, string, string, string,
		sparkwing.Logger, *slog.Logger, *orchestrator.LocalAdmission, ...orchestrator.RunNodeOption,
	) (runner.Result, error) {
		return runner.Result{}, errors.New("compile pipeline: exit status 1")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	executePooledNode(ctx, ctrl, srv.URL, "", "", sourceurl.RepoAllowlist{}, "", claimed, claimed.ClaimedBy,
		3*time.Minute, time.Hour, "pool runner", logger, nil, nil)

	got, err := st.GetNode(ctx, "r1", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != string(sparkwing.Failed) || !strings.Contains(got.Error, "compile pipeline: exit status 1") {
		t.Fatalf("node after a setup failure = outcome %q error %q, want failed with the setup error", got.Outcome, got.Error)
	}
}

// A pool runner that fetches with its own credentials claims nodes of every
// run in its team, so a node whose run names a repository its owner did not
// allow fails with the list named, and nothing is fetched.
func TestPooledNodeFromARepositoryOutsideTheAllowlistFailsNamingIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CACHE_URL", "")
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(srv.Close)
	ctrl := client.New(srv.URL, nil)
	ctx := context.Background()

	if err := st.CreateTriggerWithRun(ctx, store.Trigger{
		ID: "r1", Pipeline: "p", TriggerSource: "manual",
		RepoURL: "https://git.invalid/evil/payload.git", GitSHA: "0123456789abcdef0123456789abcdef01234567",
	}, store.Run{ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "r1", "n1"); err != nil {
		t.Fatal(err)
	}
	claimed, err := ctrl.ClaimNode(ctx, "pool:1", nil, 3*time.Minute, nil)
	if err != nil || claimed == nil {
		t.Fatalf("claim node: %+v, %v", claimed, err)
	}
	allow, err := sourceurl.ParseRepoAllowlist([]string{"github.com/acme/*"})
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	executePooledNode(ctx, ctrl, srv.URL, "", "", allow, "", claimed, claimed.ClaimedBy,
		3*time.Minute, time.Hour, "pool runner", logger, nil, nil)

	got, err := st.GetNode(ctx, "r1", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != string(sparkwing.Failed) || !strings.Contains(got.Error, "github.com/acme/*") ||
		!strings.Contains(got.Error, "git.invalid/evil/payload") {
		t.Fatalf("node = outcome %q error %q, want failed naming the repository and the allowlist", got.Outcome, got.Error)
	}
	if _, statErr := os.Stat(filepath.Join(home, "source-direct")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused node still reached the fetch: %v", statErr)
	}
}

// A direct-source pool runner sends its list with every node claim to a
// controller that advertises the field, so it never hands it a node from
// another repository.
func TestRunPoolLoopSendsTheRepositoryListWithEachNodeClaim(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent := make(chan []string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/capabilities" {
			_, _ = w.Write([]byte(`{"claims":{"allow_repos":true}}`))
			return
		}
		if r.URL.Path == "/api/v1/nodes/claim" {
			var claim struct {
				AllowRepos []string `json:"allow_repos"`
			}
			_ = json.NewDecoder(r.Body).Decode(&claim)
			select {
			case sent <- claim.AllowRepos:
			default:
			}
			cancel()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	allow, err := sourceurl.ParseRepoAllowlist([]string{"github.com/acme/*"})
	if err != nil {
		t.Fatal(err)
	}
	_ = RunPoolLoop(ctx, PoolLoopConfig{
		ControllerURL: srv.URL, AllowRepos: allow, HolderPrefix: "runner:laptop",
		PollInterval: 5 * time.Millisecond, Home: t.TempDir(),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	select {
	case got := <-sent:
		if len(got) != 1 || got[0] != "github.com/acme/*" {
			t.Fatalf("node claim carried allow_repos %q, want the runner's list", got)
		}
	default:
		t.Fatal("the pool loop made no node claim")
	}
}
