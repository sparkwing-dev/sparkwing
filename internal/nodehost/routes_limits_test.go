package nodehost_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
)

func openAwait(ctx context.Context, s interface{ URL() string }, bearer string, done chan<- int) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.URL()+"/node/v1/children/child-1/await?wait_ms=60000", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		done <- 0
		return
	}
	_ = resp.Body.Close()
	done <- resp.StatusCode
}

func TestForwardsPastTheNodesLimitAre429AndFreedOnDisconnect(t *testing.T) {
	s, _ := start(t)
	c := newChildController(t, started("child-1"))
	polled := make(chan struct{}, 64)
	c.handle("GET /api/v1/runs/run-1/children/child-1", func(w http.ResponseWriter, r *http.Request) {
		polled <- struct{}{}
		answer(http.StatusOK, map[string]string{"status": "pending"})(w, r)
	})
	c.handle("POST /api/v1/runs/run-1/oidc-token", answer(http.StatusNotFound, map[string]string{"error": "no key"}))
	bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c.fakeController))
	other, _ := admitClaim(t, s, "notify", claimFor(t, c.fakeController))
	send(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build"}`)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 4)
	for range 4 {
		go openAwait(ctx, s, bearer, done)
	}
	for range 4 {
		<-polled
	}
	before := len(c.asked())
	big := `{"pipeline":"build","args":{"a":"` + strings.Repeat("x", 1<<20) + `"}}`
	wantRefusal(t, send(t, s, http.MethodPost, "/node/v1/children", bearer, big), http.StatusTooManyRequests, "too_many_requests")
	wantRefusal(t, send(t, s, http.MethodPost, "/node/v1/oidc-token", bearer, `{"audience":"a"}`), http.StatusTooManyRequests, "too_many_requests")
	wantRefusal(t, send(t, s, http.MethodGet, "/node/v1/nodes/build/output", bearer, ""), http.StatusTooManyRequests, "too_many_requests")
	for _, p := range c.asked()[before:] {
		if !strings.HasSuffix(p, "/children/child-1") {
			t.Fatalf("a refused request reached the controller: %s", p)
		}
	}
	wantRefusal(t, send(t, s, http.MethodPost, "/node/v1/oidc-token", other, `{"audience":"a"}`), http.StatusNotFound, "oidc_unavailable")

	cancel()
	for range 4 {
		<-done
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := send(t, s, http.MethodPost, "/node/v1/oidc-token", bearer, `{"audience":"a"}`)
		if got.status == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disconnected awaits kept their places: status %d body %v", got.status, got.body)
		}
	}
}

func TestChildBodyOverTheLimitIsRefused(t *testing.T) {
	s, _ := start(t)
	c := newChildController(t)
	bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c.fakeController))
	body := `{"pipeline":"build","args":{"a":"` + strings.Repeat("x", 64<<10) + `"}}`
	wantRefusal(t, send(t, s, http.MethodPost, "/node/v1/children", bearer, body), http.StatusBadRequest, "bad_request")
	if len(c.asked()) != 0 {
		t.Fatalf("an oversized body reached the controller: %v", c.asked())
	}
}

func TestANodeHoldsOneOutputAtATime(t *testing.T) {
	s, _ := start(t)
	c := newController(t)
	grant := c.object("unused", []byte(`"big"`))
	grant.URL = "/objects/big"
	release, fetching, granted := make(chan struct{}), make(chan struct{}, 2), make(chan struct{}, 2)
	var inFlight, most, fetches atomic.Int32
	c.handle("GET /objects/big", func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		n := inFlight.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		fetching <- struct{}{}
		<-release
		inFlight.Add(-1)
		_, _ = w.Write([]byte(`"big"`))
	})
	c.handle("GET /api/v1/runs/run-1/nodes/{node}", nodeRow("done", "success"))
	c.handle("GET /api/v1/runs/run-1/nodes/{node}/output", func(w http.ResponseWriter, r *http.Request) {
		granted <- struct{}{}
		answer(http.StatusOK, grant)(w, r)
	})
	bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c))
	read := func(ctx context.Context, status chan<- int) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.URL()+"/node/v1/nodes/build/output", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- 0
			return
		}
		_ = resp.Body.Close()
		status <- resp.StatusCode
	}

	first, second := make(chan int, 1), make(chan int, 1)
	go read(context.Background(), first)
	<-granted
	<-fetching
	ctx, cancel := context.WithCancel(context.Background())
	go read(ctx, second)
	<-granted
	cancel()
	<-second
	close(release)
	if got := <-first; got != http.StatusOK {
		t.Fatalf("first read answered %d", got)
	}
	if most.Load() != 1 || fetches.Load() != 1 {
		t.Fatalf("%d outputs in the host at once over %d fetches, want 1 and 1; a waiting read must not fetch",
			most.Load(), fetches.Load())
	}
}
