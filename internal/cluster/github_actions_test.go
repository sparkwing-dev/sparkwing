package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestGitHubActionsCredentialAsksForTheControllerAudienceAndNamesTheTeam(t *testing.T) {
	expires := time.Now().Add(time.Hour).Unix()
	var ctrlURL string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer request-secret" || r.URL.Query().Get("audience") != ctrlURL {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"value": "id-token"})
	})
	mux.HandleFunc("POST /api/v1/runners/github/exchange", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["id_token"] != "id-token" || body["team"] != "acme" {
			http.Error(w, "bad exchange", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(githubCredential{
			Token: "swr_x", Team: "acme", Repository: "acme/widgets", ExpiresAt: expires, Labels: []string{"github-actions"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctrlURL = srv.URL
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", srv.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-secret")

	cred, err := githubActionsCredential(context.Background(), srv.Client(), srv.URL+"/", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if cred.Token != "swr_x" || cred.Repository != "acme/widgets" {
		t.Fatalf("credential = %+v", cred)
	}
	if got := githubClaimDeadline(cred).Unix(); got != expires-int64((10*time.Minute)/time.Second) {
		t.Fatalf("claims stop at %d, want ten minutes before credential expiry %d", got, expires)
	}
	if _, err := githubActionsCredential(context.Background(), srv.Client(), srv.URL, "other"); err == nil ||
		!strings.Contains(err.Error(), "400") {
		t.Fatalf("exchange for a team the controller refuses = %v, want its 400", err)
	}
}

func TestGitHubActionsCredentialNeedsTheIDTokenPermission(t *testing.T) {
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	_, err := githubActionsCredential(context.Background(), http.DefaultClient, "http://ctrl", "acme")
	if err == nil || !strings.Contains(err.Error(), "id-token: write") {
		t.Fatalf("err = %v, want a pointer at the workflow permission", err)
	}
}

func TestGitHubActionsRunnerClaimsTriggersInProcess(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_WARM_MODULES", "off")
	t.Setenv("SPARKWING_TRIGGER_RUNNER", "")
	var triggerClaims atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"value": "id-token"})
	})
	mux.HandleFunc("POST /api/v1/runners/github/exchange", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(githubCredential{
			Token: "swr_x", Team: "acme", Repository: "acme/widgets",
			ExpiresAt: time.Now().Add(time.Hour).Unix(), Labels: []string{"github-actions"},
		})
	})
	mux.HandleFunc("POST /api/v1/triggers/claim", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NodeRunner string `json:"node_runner"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.NodeRunner != "inprocess" {
			t.Errorf("trigger claim body = %+v, %v; want inprocess", body, err)
		}
		triggerClaims.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/v1/nodes/claim", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", srv.URL+"/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-secret")

	err := runRunnerCLI([]string{
		"--controller=" + srv.URL, "--team=acme", "--github-actions",
		"--metrics-addr=", "--poll=10ms", "--idle-exit=100ms",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if triggerClaims.Load() == 0 {
		t.Fatal("Actions runner exited without claiming triggers")
	}
}

func TestRunPoolLoop_IdleExitWaitsForHeldNodesThenLeaves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var running atomic.Bool
		polling := make(chan time.Time, 100)
		responses := []claimResp{{node: fakeNode("a")}}
		for range 100000 {
			responses = append(responses, claimResp{})
		}
		stub := &stubClaimer{responses: responses, observe: func() {
			if running.Load() {
				select {
				case polling <- time.Now():
				default:
				}
			}
		}}
		release := make(chan struct{})
		released := false
		releaseNode := func() {
			if !released {
				close(release)
				released = true
			}
		}
		defer releaseNode()
		started := make(chan struct{})
		exec := func(ctx context.Context, n *store.Node, holderID string) {
			running.Store(true)
			close(started)
			<-release
		}
		cfg := normalizePoolLoopConfig(PoolLoopConfig{
			ControllerURL: "http://stub", HolderPrefix: "test", MaxConcurrent: 2,
			PollInterval: time.Millisecond, IdleExit: 20 * time.Millisecond, SourceName: "test runner",
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = runPoolLoop(ctx, cfg, stub, exec, nil, discardLogger())
		}()

		select {
		case <-started:
		case <-done:
			t.Fatal("the loop exited before its claimed node started")
		case <-ctx.Done():
			t.Fatal("the claimed node never started")
		}
		heldAt := time.Now()
		held, stopHeld := context.WithTimeout(ctx, 2*cfg.IdleExit)
		defer stopHeld()
		<-held.Done()
		synctest.Wait()
		if ctx.Err() != nil {
			t.Fatal("the loop stopped before the held node passed idle exit")
		}
		select {
		case <-done:
			t.Fatal("the loop exited while a node was still held")
		default:
		}
		latestPoll := time.Time{}
		for len(polling) > 0 {
			latestPoll = <-polling
		}
		if latestPoll.Before(heldAt.Add(cfg.IdleExit)) {
			t.Fatal("the loop stopped polling before the held node passed idle exit")
		}
		releaseNode()
		exitCtx, stopExit := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopExit()
		select {
		case <-done:
		case <-exitCtx.Done():
			t.Fatal("the loop did not exit after the held node finished and the queue stayed empty")
		}
		if exitCtx.Err() != nil {
			t.Fatal("the loop reached the idle-exit deadline before it stopped")
		}
		if ctx.Err() != nil {
			t.Fatal("the loop ran until cancelled instead of exiting when idle")
		}
	})
}

func TestRunPoolLoop_ClaimUntilStopsNewClaims(t *testing.T) {
	stub := &stubClaimer{responses: []claimResp{{node: fakeNode("a")}}}
	cfg := normalizePoolLoopConfig(PoolLoopConfig{
		ControllerURL: "http://stub", HolderPrefix: "test", MaxConcurrent: 1,
		PollInterval: time.Millisecond, ClaimUntil: time.Now().Add(-time.Second), SourceName: "test runner",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runPoolLoop(ctx, cfg, stub, func(context.Context, *store.Node, string) {}, nil, discardLogger()); err != nil {
		t.Fatal(err)
	}
	if got := stub.calls.Load(); got != 0 || ctx.Err() != nil {
		t.Fatalf("claims after the deadline = %d (ctx %v), want none and a prompt exit", got, ctx.Err())
	}
}
