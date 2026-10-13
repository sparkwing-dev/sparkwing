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

func openRequest(ctx context.Context, s interface{ URL() string }, method, path, bearer, body string, done chan<- int) {
	req, _ := http.NewRequestWithContext(ctx, method, s.URL()+path, strings.NewReader(body))
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
	c := newChildController(t)
	entered, release := make(chan struct{}, 8), make(chan struct{})
	c.handle("POST /api/v1/runs/run-1/oidc-token", func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		answer(http.StatusNotFound, map[string]string{"error": "no key"})(w, r)
	})
	bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c.fakeController))
	other, _ := admitClaim(t, s, "notify", claimFor(t, c.fakeController))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 4)
	for range 4 {
		go openRequest(ctx, s, http.MethodPost, "/node/v1/oidc-token", bearer, `{"audience":"a"}`, done)
	}
	for range 4 {
		<-entered
	}
	before := len(c.asked())
	big := `{"pipeline":"build","args":{"a":"` + strings.Repeat("x", 1<<20) + `"}}`
	wantRefusal(t, ask(t, s, http.MethodPost, "/node/v1/children", bearer, big), http.StatusTooManyRequests, "too_many_requests")
	wantRefusal(t, ask(t, s, http.MethodPost, "/node/v1/oidc-token", bearer, `{"audience":"a"}`), http.StatusTooManyRequests, "too_many_requests")
	wantRefusal(t, ask(t, s, http.MethodGet, "/node/v1/nodes/build/output", bearer, ""), http.StatusTooManyRequests, "too_many_requests")
	wantRefusal(t, ask(t, s, http.MethodGet, "/node/v1/pipelines/p/latest?node=n", bearer, ""), http.StatusTooManyRequests, "too_many_requests")
	if asked := c.asked()[before:]; len(asked) != 0 {
		t.Fatalf("a refused request reached the controller: %v", asked)
	}
	go openRequest(context.Background(), s, http.MethodPost, "/node/v1/oidc-token", other, `{"audience":"a"}`, done)
	<-entered

	cancel()
	for range 4 {
		<-done
	}
	close(release)
	if got := <-done; got != http.StatusNotFound {
		t.Fatalf("another node's request answered %d while this node was full", got)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := ask(t, s, http.MethodPost, "/node/v1/oidc-token", bearer, `{"audience":"a"}`)
		if got.status == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disconnected requests kept their places: status %d body %v", got.status, got.body)
		}
	}
}

func TestChildBodyOverTheLimitIsRefused(t *testing.T) {
	s, _ := start(t)
	c := newChildController(t)
	bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c.fakeController))
	body := `{"pipeline":"build","args":{"a":"` + strings.Repeat("x", 64<<10) + `"}}`
	wantRefusal(t, ask(t, s, http.MethodPost, "/node/v1/children", bearer, body), http.StatusBadRequest, "bad_request")
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
