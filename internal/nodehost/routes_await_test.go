package nodehost_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTenAwaitsOnTenChildrenAllSucceed(t *testing.T) {
	s, _ := start(t)
	var replies []http.HandlerFunc
	for i := range 10 {
		replies = append(replies, started(fmt.Sprintf("child-%d", i)))
	}
	c := newChildController(t, replies...)
	var mu sync.Mutex
	polls := map[string]int{}
	firstPoll := make(chan struct{}, 10)
	c.handle("GET /api/v1/runs/run-1/children/{child}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		polls[r.PathValue("child")]++
		n := polls[r.PathValue("child")]
		mu.Unlock()
		if n == 1 {
			firstPoll <- struct{}{}
			answer(http.StatusOK, map[string]string{"status": "running"})(w, r)
			return
		}
		answer(http.StatusOK, map[string]string{"status": "success"})(w, r)
	})
	bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c.fakeController))
	for range 10 {
		send(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build"}`)
	}

	done := make(chan int, 10)
	for i := range 10 {
		go openRequest(context.Background(), s, http.MethodGet, fmt.Sprintf("/node/v1/children/child-%d/await?wait_ms=60000", i), bearer, "", done)
	}
	for range 10 {
		<-firstPoll
	}
	for range 10 {
		if got := <-done; got != http.StatusOK {
			t.Fatalf("an await answered %d", got)
		}
	}
}

func TestAwaitsOnOneChildShareOnePoller(t *testing.T) {
	s, _ := start(t)
	c := newChildController(t, started("child-1"))
	var polls atomic.Int32
	firstPoll, secondPoll, finish := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
	c.handle("GET /api/v1/runs/run-1/children/child-1", func(w http.ResponseWriter, r *http.Request) {
		switch polls.Add(1) {
		case 1:
			firstPoll <- struct{}{}
			answer(http.StatusOK, map[string]string{"status": "running"})(w, r)
		case 2:
			secondPoll <- struct{}{}
			<-finish
			answer(http.StatusOK, map[string]string{"status": "success"})(w, r)
		default:
			answer(http.StatusOK, map[string]string{"status": "success"})(w, r)
		}
	})
	bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c.fakeController))
	send(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build"}`)

	const awaits = 64
	done := make(chan int, awaits)
	path := "/node/v1/children/child-1/await?wait_ms=60000"
	go openRequest(context.Background(), s, http.MethodGet, path, bearer, "", done)
	<-firstPoll
	for range awaits - 1 {
		go openRequest(context.Background(), s, http.MethodGet, path, bearer, "", done)
	}
	// safety: a probe that is admitted answers at once from the poll already
	// made, so a 429 means all 64 awaits hold their places.
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := send(t, s, http.MethodGet, "/node/v1/children/child-1/await?wait_ms=0", bearer, "")
		if got.status == http.StatusTooManyRequests && got.body["code"] == "too_many_requests" {
			break
		}
		if got.status != http.StatusOK || time.Now().After(deadline) {
			t.Fatalf("probe: status %d body %v", got.status, got.body)
		}
	}
	<-secondPoll
	close(finish)
	for range awaits {
		if got := <-done; got != http.StatusOK {
			t.Fatalf("an await answered %d", got)
		}
	}
	if n := polls.Load(); n != 2 {
		t.Fatalf("%d awaits made %d polls, want 2: one poller a second", awaits, n)
	}
	got := send(t, s, http.MethodGet, path, bearer, "")
	if got.status != http.StatusOK || got.body["status"] != "success" || polls.Load() != 3 {
		t.Fatalf("an await after the child ended: status %d body %v, %d polls; the ended poller must not serve it",
			got.status, got.body, polls.Load())
	}
}

func TestAwaitPollerStopsOnRevokeAndClose(t *testing.T) {
	for _, end := range []string{"revoke", "close"} {
		t.Run(end, func(t *testing.T) {
			s, _ := start(t)
			c := newChildController(t, started("child-1"))
			polling, stopped := make(chan struct{}, 1), make(chan struct{}, 1)
			c.handle("GET /api/v1/runs/run-1/children/child-1", func(_ http.ResponseWriter, r *http.Request) {
				polling <- struct{}{}
				<-r.Context().Done()
				stopped <- struct{}{}
			})
			bearer, revoke, err := s.Admit(nodeWithClaim("deploy", claimFor(t, c.fakeController)))
			if err != nil {
				t.Fatal(err)
			}
			send(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build"}`)
			done := make(chan int, 1)
			go openRequest(context.Background(), s, http.MethodGet, "/node/v1/children/child-1/await?wait_ms=60000", bearer, "", done)
			<-polling
			if end == "revoke" {
				revoke()
			} else if err := s.Close(); err != nil {
				t.Fatalf("Close left a request in flight: %v", err)
			}
			<-stopped
			<-done
		})
	}
}
