package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type headerCapture struct {
	mu   sync.Mutex
	seen map[string]string
}

func (h *headerCapture) record(path, identity string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen[path] = identity
}

func (h *headerCapture) get(path string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seen[path]
}

// Every route the controller budgets as a claim has to carry the runner's
// identity, because those routes name no run, node or agent the controller
// could derive one from. A method that forgets it drops its caller into the
// bucket every nameless runner shares.
func TestRunnerIdentity_EveryClaimMethodNamesItsRunner(t *testing.T) {
	capture := &headerCapture{seen: map[string]string{}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.record(r.URL.Path+"|"+r.Method, r.Header.Get(store.RunnerIdentityHeader))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	const identity = "runner:test-host"
	c := client.New(ts.URL, nil).WithRunnerIdentity(identity)
	ctx := context.Background()

	calls := []struct {
		name  string
		route string
		call  func() error
	}{
		{
			name: "ClaimNode", route: "/api/v1/nodes/claim|POST",
			call: func() error {
				_, err := c.ClaimNode(ctx, "holder-1", nil, time.Minute, nil)
				return err
			},
		},
		{
			name: "ClaimTriggerFor", route: "/api/v1/triggers/claim|POST",
			call: func() error {
				_, err := c.ClaimTriggerFor(ctx, nil, nil)
				return err
			},
		},
		{
			name: "PrepareExecutorClaim", route: "/api/v1/nodes/claim/prepare|POST",
			call: func() error {
				_, err := c.PrepareExecutorClaim(ctx, "agent-1")
				return err
			},
		},
		{
			name: "OfferExecutorClaim", route: "/api/v1/nodes/claim|POST",
			call: func() error {
				_, err := c.OfferExecutorClaim(ctx,
					client.ExecutorClaim{ExecutorName: "agent-1", HolderID: "holder-1"}, "run-1", "node-1")
				return err
			},
		},
		{
			name: "HeartbeatNodeClaim", route: "/api/v1/runs/run-1/nodes/node-1/heartbeat|POST",
			call: func() error {
				return c.HeartbeatNodeClaim(ctx, "run-1", "node-1", "holder-1", time.Minute, nil)
			},
		},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			capture.record(tc.route, "")
			if err := tc.call(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got := capture.get(tc.route); got != identity {
				t.Errorf("%s sent %s=%q, want %q", tc.name, store.RunnerIdentityHeader, got, identity)
			}
		})
	}
}

func TestRunnerIdentity_IsSafeToSetWhileTheClientServes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := client.New(ts.URL, nil)
	if got := c.RunnerIdentity(); got != "" {
		t.Errorf("a client given no identity reports %q", got)
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				c.WithRunnerIdentity("runner-" + time.Now().Format("150405.000000000"))
				return
			}
			_, _ = c.ClaimNode(context.Background(), "holder-1", nil, time.Minute, nil)
		}()
	}
	wg.Wait()
	if got := c.RunnerIdentity(); got == "" {
		t.Error("the identity set while the client was serving did not stick")
	}
}
