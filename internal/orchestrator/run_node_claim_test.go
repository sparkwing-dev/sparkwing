//go:build unix

package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: the claim reaches run-node as a dispatcher's Job hands it, in the environment, with the controller as
// a flag.
type claimedCommand struct {
	h       *hostedTrigger
	nodeID  string
	claimed *store.Node
	wrap    func(w http.ResponseWriter, r *http.Request, next http.Handler)
	url     string
}

func newClaimedCommand(t *testing.T, args map[string]string, nodeID string) *claimedCommand {
	t.Helper()
	h := newHostedTriggerWith(t, args, func(trig *store.Trigger) { trig.GithubOwner, trig.GithubRepo = "acme", "widgets" })
	c := &claimedCommand{h: h, nodeID: nodeID}
	rc, err := triggerRunContext(t.Context(), client.NewWithToken(h.url, nil, h.token), h.trig)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := nodehost.PlanDocument(t.Context(), nodehost.SessionConfig{Entry: pipelineentry.Binary(h.bin), Dir: t.TempDir()},
		nodehost.PlanRequest{Pipeline: "podnode", Args: args, Run: rc})
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	for _, n := range planned.Doc.Nodes {
		if n.ID == nodeID {
			hash = n.SpecHash
		}
	}
	ctx := store.WithoutCreditMetering(t.Context())
	if err := h.st.UpdatePlanSnapshot(ctx, h.trig.ID, planned.JSON); err != nil {
		t.Fatal(err)
	}
	if err := h.st.CreateNode(ctx, store.Node{RunID: h.trig.ID, NodeID: nodeID, Status: "pending", SpecHash: hash}); err != nil {
		t.Fatal(err)
	}
	handler := controller.New(h.st, slog.New(slog.DiscardHandler)).EnableAuthFromStore().Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.wrap != nil {
			c.wrap(w, r, handler)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c.url = srv.URL
	c.claimed, err = client.NewWithToken(h.url, nil, h.token).ClaimNodeByID(ctx, h.trig.ID, nodeID, "k8s-job:sw-"+nodeID, time.Minute, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// safety: only the source fetch and build are stood in for, with the prebuilt pipeline; the command's claim,
// renewal and RunNodeOnce's source check all run as a dispatcher's Job runs them. The environment is set before
// start, on the test's goroutine.
func (c *claimedCommand) start(t *testing.T, ctx context.Context) <-chan error {
	t.Helper()
	for _, name := range []string{
		"SPARKWING_CONTROLLER_URL", "SPARKWING_RUN_ID", "SPARKWING_NODE_ID", authwire.CacheGrantEnv,
		"SPARKWING_GITCACHE_URL", "SPARKWING_DRY_RUN", "SPARKWING_START_AT", "SPARKWING_STOP_AT",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("SPARKWING_AGENT_TOKEN", c.claimed.ClaimToken)
	t.Setenv("SPARKWING_NODE_CLAIM_HOLDER", c.claimed.ClaimedBy)
	t.Setenv("SPARKWING_NODE_CLAIM_GENERATION", strconv.FormatInt(c.claimed.ClaimGeneration, 10))
	t.Setenv("SPARKWING_NODE_CLAIM_MEMBERSHIP", c.claimed.ClaimMembershipID)
	t.Setenv("SPARKWING_NODE_CLAIM_RESERVATION", c.claimed.ReservationID)
	t.Setenv("SPARKWING_NODE_CLAIM_LEASE_SECONDS", "600")
	dir := t.TempDir()
	host := withRemoteNodeHost(func(ctx context.Context, _ *store.Trigger, _ *store.Run, controllerURL, _, cacheGrant, runID, nodeID, token string,
		_ *sourceurl.RepoAllowlist, logger *slog.Logger,
	) (runner.Result, error) {
		return hostDispatchedNode(ctx, podNode{
			entry: pipelineentry.Binary(c.h.bin), dir: dir, controllerURL: controllerURL, token: token,
			runID: runID, nodeID: nodeID, cacheGrant: cacheGrant, logger: logger,
		})
	})
	ctx, cancel := context.WithCancel(ctx)
	done, result := make(chan struct{}), make(chan error, 1)
	// safety: a test that gives up still stops the command and waits for it, so no renewal outlives the test.
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		result <- runNodeCommand(ctx, []string{"--controller", c.url, c.h.trig.ID, c.nodeID}, host)
	}()
	return result
}

// A dispatched node's every write reaches the controller under the claim the
// dispatcher took for it, so a write from a holder that lost the claim is
// refused rather than taken as the node's.
func TestRunNodeCommand_SendsTheDispatchedClaimFence(t *testing.T) {
	c := newClaimedCommand(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "typed": "true"}, "typed")
	var mu sync.Mutex
	seen, plain := map[string]string{}, []string{}
	c.wrap = func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/nodes/") && !strings.HasSuffix(r.URL.Path, "/claim") {
			mu.Lock()
			if holder := r.Header.Get(store.ClaimHolderHeader); holder != "" {
				seen[r.URL.Path] = holder + "/" + r.Header.Get(store.ClaimGenerationHeader)
			} else {
				plain = append(plain, r.URL.Path)
			}
			mu.Unlock()
		}
		next.ServeHTTP(w, r)
	}
	if err := <-c.start(t, t.Context()); err != nil {
		t.Fatalf("run-node: %v", err)
	}
	if n := c.h.node(t, "typed"); n.Status != "done" || n.Outcome != string(sparkwing.Success) {
		t.Fatalf("typed = %s/%s (%s), want done/success", n.Status, n.Outcome, n.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	want := c.claimed.ClaimedBy + "/" + strconv.FormatInt(c.claimed.ClaimGeneration, 10)
	if len(plain) > 0 || len(seen) == 0 {
		t.Fatalf("node writes without the fence %v; fenced %v", plain, seen)
	}
	for path, got := range seen {
		if got != want {
			t.Errorf("%s carried fence %q, want %q", path, got, want)
		}
	}
	for _, route := range []string{"start", "output-upload", "output-commit", "finish"} {
		if _, ok := seen["/api/v1/runs/"+c.h.trig.ID+"/nodes/typed/"+route]; !ok {
			t.Errorf("the node made no fenced %s write; fenced %v", route, seen)
		}
	}
}

// The controller refuses a renewal when the team's credits run out, the claim
// was reaped or the node was cancelled; the pod stops the running node and
// exits non-zero rather than bill compute until the Job's deadline.
func TestRunNodeCommand_StopsWhenTheClaimRenewalIsRefused(t *testing.T) {
	hold, pids := heldNodePID(t)
	c := newClaimedCommand(t, map[string]string{"envout": filepath.Join(t.TempDir(), "env"), "hold": hold}, "held")
	// safety: unbuffered, so the refusal is answered only after the test has taken the start, and the command
	// cannot end first.
	started, ended := make(chan int), make(chan struct{})
	// safety: registered after the controller's Close, so it runs first and frees a handler still holding a renewal.
	t.Cleanup(func() { close(ended) })
	c.wrap = func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/nodes/held/heartbeat") {
			// safety: the refusal waits for the node's body, so it stops a running node, not one yet to start.
			var pid int
			select {
			case pid = <-pids:
			case <-r.Context().Done():
				return
			case <-ended:
				return
			}
			select {
			case started <- pid:
			case <-r.Context().Done():
				return
			case <-ended:
				return
			}
			http.Error(w, `{"error":"insufficient credits"}`, http.StatusConflict)
			return
		}
		next.ServeHTTP(w, r)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	done := c.start(t, ctx)
	var pid int
	select {
	case pid = <-started:
	case err := <-done:
		t.Fatalf("run-node returned %v before the held node started", err)
	case <-ctx.Done():
		t.Fatal("the held node never started")
	}
	select {
	case err := <-done:
		if !errors.Is(err, errClaimAbandoned) {
			t.Fatalf("run-node = %v, want it to fail on the refused renewal", err)
		}
	case <-ctx.Done():
		t.Fatal("the node kept running after the controller refused its claim renewal")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the held node's process %d outlived the refused renewal: %v", pid, err)
	}
}
