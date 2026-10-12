package nodehost_test

import (
	"net/http"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type childController struct {
	*fakeController
	mu       sync.Mutex
	replies  []http.HandlerFunc
	ordinals []int64
}

func newChildController(t *testing.T, replies ...http.HandlerFunc) *childController {
	c := &childController{fakeController: newController(t), replies: replies}
	c.handle("POST /api/v1/runs/run-1/children", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Ordinal int64 `json:"ordinal"`
		}
		_ = decodeJSON(r, &body)
		c.mu.Lock()
		c.ordinals = append(c.ordinals, body.Ordinal)
		next := c.replies[0]
		c.replies = c.replies[1:]
		c.mu.Unlock()
		next(w, r)
	})
	return c
}

func started(id string) http.HandlerFunc {
	return answer(http.StatusAccepted, map[string]string{"run_id": id, "status": "dispatched"})
}

func TestChildStartsAndAwaitsItsOutput(t *testing.T) {
	s, logs := start(t)
	c := newChildController(t, started("child-1"), started("child-2"))
	grant := c.object("child-out", []byte(`{"image":"app:1"}`))
	c.handle("GET /api/v1/runs/run-1/children/child-1", answer(http.StatusOK, map[string]string{"id": "child-1", "status": "success"}))
	c.handle("GET /api/v1/runs/run-1/children/child-1/nodes/build/output", answer(http.StatusOK, grant))
	c.handle("GET /api/v1/runs/run-1/children/child-2", answer(http.StatusOK, map[string]string{"status": "running"}))
	bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c.fakeController))

	got := ask(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build","args":{"tag":"1"}}`)
	if got.status != http.StatusCreated || got.body["run_id"] != "child-1" {
		t.Fatalf("start: status %d body %v", got.status, got.body)
	}
	got = ask(t, s, http.MethodGet, "/node/v1/children/child-1/await?node=build", bearer, "")
	if got.status != http.StatusOK || got.body["status"] != "success" ||
		got.body["output"].(map[string]any)["image"] != "app:1" {
		t.Fatalf("await: status %d body %v", got.status, got.body)
	}

	ask(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build"}`)
	got = ask(t, s, http.MethodGet, "/node/v1/children/child-2/await?node=build&wait_ms=0", bearer, "")
	if got.status != http.StatusOK || got.body["status"] != "running" || got.body["output"] != nil {
		t.Fatalf("await running: status %d body %v", got.status, got.body)
	}
	if len(c.ordinals) != 2 || c.ordinals[0] != 0 || c.ordinals[1] != 1 {
		t.Fatalf("ordinals %v, want [0 1]", c.ordinals)
	}
	noLeak(t, logs)
}

func TestChildOrdinalSurvivesAFailedCall(t *testing.T) {
	s, logs := start(t)
	c := newChildController(t, leak, started("child-1"), answer(http.StatusConflict, map[string]string{"error": "cycle: build would re-enter itself"}), started("child-2"))
	bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c.fakeController))

	wantRefusal(t, ask(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build"}`), http.StatusBadGateway, "upstream_unavailable")
	if got := ask(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build"}`); got.status != http.StatusCreated {
		t.Fatalf("retry: status %d body %v", got.status, got.body)
	}
	wantRefusal(t, ask(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"deploy"}`), http.StatusConflict, "cycle")
	ask(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build"}`)
	if want := []int64{0, 0, 1, 2}; len(c.ordinals) != 4 || c.ordinals[0] != want[0] || c.ordinals[1] != want[1] ||
		c.ordinals[2] != want[2] || c.ordinals[3] != want[3] {
		t.Fatalf("ordinals %v, want %v", c.ordinals, want)
	}
	noLeak(t, logs)
}

func TestChildStartRefusals(t *testing.T) {
	cases := []struct {
		name, body string
		reply      http.HandlerFunc
		status     int
		code       string
	}{
		{
			"other repository", `{"pipeline":"build","repo":"elsewhere"}`,
			answer(http.StatusUnprocessableEntity, map[string]string{"error": "own repository only"}), http.StatusUnprocessableEntity, "child_repo",
		},
		{
			"ordinal conflict", `{"pipeline":"build"}`,
			answer(http.StatusConflict, map[string]string{"error": "invocation 0 already started a child"}), http.StatusBadGateway, "upstream_unavailable",
		},
		{
			"claim ended", `{"pipeline":"build"}`,
			answer(http.StatusForbidden, map[string]string{"error": "claim_ended"}), http.StatusBadGateway, "upstream_unavailable",
		},
		{"no pipeline", `{}`, nil, http.StatusBadRequest, "bad_request"},
		{"slash in pipeline", `{"pipeline":"a/b"}`, nil, http.StatusBadRequest, "bad_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, logs := start(t)
			var replies []http.HandlerFunc
			if tc.reply != nil {
				replies = append(replies, tc.reply)
			}
			c := newChildController(t, replies...)
			bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c.fakeController))
			wantRefusal(t, ask(t, s, http.MethodPost, "/node/v1/children", bearer, tc.body), tc.status, tc.code)
			if tc.reply == nil && len(c.asked()) > 0 {
				t.Fatalf("a malformed request reached the controller: %v", c.asked())
			}
			noLeak(t, logs)
		})
	}
}

func TestAwaitRefusesARunThisNodeDidNotStart(t *testing.T) {
	s, logs := start(t)
	c := newChildController(t, started("child-1"))
	c.handle("GET /api/v1/runs/run-1/children/{child}", answer(http.StatusOK, map[string]string{"status": "success"}))
	c.handle("GET /api/v1/runs/run-1/children/{child}/nodes/{node}/output", answer(http.StatusOK, store.OutputReadGrant{}))
	starter, _ := admitClaim(t, s, "deploy", claimFor(t, c.fakeController))
	other, _ := admitClaim(t, s, "notify", claimFor(t, c.fakeController))
	ask(t, s, http.MethodPost, "/node/v1/children", starter, `{"pipeline":"build"}`)
	before := len(c.asked())

	for _, path := range []string{
		"/node/v1/children/child-1/await",
		"/node/v1/children/run-1/await",
		"/node/v1/children/some-other-teams-run/await",
	} {
		wantRefusal(t, ask(t, s, http.MethodGet, path, other, ""), http.StatusNotFound, "unknown_child")
	}
	if asked := c.asked()[before:]; len(asked) != 0 {
		t.Fatalf("an unknown child reached the controller: %v", asked)
	}
	for _, path := range []string{
		"/node/v1/children/child-1/await?wait_ms=60001",
		"/node/v1/children/child-1/await?wait_ms=-1",
		"/node/v1/children/child-1/await?node=..",
	} {
		wantRefusal(t, ask(t, s, http.MethodGet, path, starter, ""), http.StatusBadRequest, "bad_request")
	}
	if got := ask(t, s, http.MethodGet, "/node/v1/children/child-1/await?node=build", starter, ""); got.status != http.StatusOK {
		t.Fatalf("starter's own await: status %d body %v", got.status, got.body)
	}
	noLeak(t, logs)
}

func TestAwaitEndsWhenTheGrantIsRevoked(t *testing.T) {
	s, _ := start(t)
	c := newChildController(t, started("child-1"))
	polled := make(chan struct{}, 1)
	c.handle("GET /api/v1/runs/run-1/children/child-1", func(w http.ResponseWriter, r *http.Request) {
		select {
		case polled <- struct{}{}:
		default:
		}
		answer(http.StatusOK, map[string]string{"status": "pending"})(w, r)
	})
	bearer, revoke, err := s.Admit(nodeWithClaim("deploy", claimFor(t, c.fakeController)))
	if err != nil {
		t.Fatal(err)
	}
	ask(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build"}`)

	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, s.URL()+"/node/v1/children/child-1/await?wait_ms=60000", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- 0
			return
		}
		_ = resp.Body.Close()
		done <- resp.StatusCode
	}()
	<-polled
	revoke()
	if status := <-done; status != http.StatusBadGateway {
		t.Fatalf("revoked await answered %d, want 502", status)
	}
}
