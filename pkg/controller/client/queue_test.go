package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestQueueManagerUsesFinalRoutes(t *testing.T) {
	calls := 0
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if req.Method != "GET" || req.URL.RequestURI() != "/api/v1/queue" {
				t.Fatalf("queue: %s %s", req.Method, req.URL)
			}
			return engineResponse(200, `{"resources":[{"key":"cores","capacity":2.5,"held":1}],"holders":[{"run_id":"r1","elapsed_ms":0,"resources":{}}],"waiters":[{"run_id":"r2","position":1,"resources":{}}],"runners":[{"name":"linux","cores":1.5,"memory_bytes":1024}]}`), nil
		case 2:
			if req.Method != "GET" || req.URL.EscapedPath() != "/api/v1/queue/groups/deploy%2Fprod%20+" {
				t.Fatalf("group: %s %s", req.Method, req.URL)
			}
			return engineResponse(200, `{"group":"deploy/prod +","capacity":2,"effective_capacity":2,"used_cost":1,"holders":[{"holder_id":"h1","run_id":"r1","job_id":"deploy","claimed_at":"2026-10-14T01:00:00Z","lease_expires_at":"2026-10-14T02:00:00Z","superseded":false,"cost":1}],"waiters":[{"run_id":"r2","job_id":"deploy","arrived_at":"2026-10-14T01:01:00Z","policy":"queue","position":1,"cost":1}]}`), nil
		case 3:
			if req.Header.Get("Idempotency-Key") != "stable" {
				t.Fatal("mutation key lost")
			}
			if req.Method != "PUT" || req.URL.EscapedPath() != "/api/v1/queue/groups/deploy%2Fprod%20+/priority" {
				t.Fatalf("priority: %s %s", req.Method, req.URL)
			}
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["run_id"] != "r2" || body["priority"] != float64(0) || len(body) != 2 {
				t.Fatalf("explicit zero lost: %+v", body)
			}
			return engineResponse(200, `{"group":"deploy/prod +","run_id":"r2","found":true,"previous":3,"priority":0,"position":1,"previous_position":2,"participants":1}`), nil
		default:
			t.Fatal("unexpected request")
			return nil, io.EOF
		}
	})
	qs, err := c.ReadQueue(t.Context())
	if err != nil || len(qs.Resources) != 1 || qs.Resources[0].Capacity != 2.5 || len(qs.Waiters) != 1 || len(qs.Runners) != 1 {
		t.Fatalf("queue: %+v %v", qs, err)
	}
	group, err := c.ReadQueueGroup(t.Context(), "deploy/prod +")
	if err != nil || group.Group != "deploy/prod +" || len(group.Holders) != 1 || group.Holders[0].JobID != "deploy" || len(group.Waiters) != 1 || group.Waiters[0].Position != 1 {
		t.Fatalf("group: %+v %v", group, err)
	}
	zero := 0
	result, err := c.SetGroupPriority(t.Context(), "deploy/prod +", controllerwire.QueuePriorityWrite{RunID: "r2", Priority: &zero, IdempotencyKey: "stable"})
	if err != nil || !result.Found || result.Priority != 0 || result.Previous != 3 || result.PreviousPosition != 2 || calls != 3 {
		t.Fatalf("priority: %+v %v calls=%d", result, err, calls)
	}
}

func TestQueueManagerRejectsInvalidInputsBeforeHTTP(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) { t.Fatal("invalid input reached HTTP"); return nil, io.EOF })
	for _, group := range []string{"", " ", ".", "..", "bad\nname"} {
		if _, err := c.ReadQueueGroup(t.Context(), group); err == nil {
			t.Fatalf("accepted group %q", group)
		}
	}
	zero := 0
	for _, input := range []controllerwire.QueuePriorityWrite{
		{}, {RunID: "r1"}, {RunID: " ", Mode: "front"}, {RunID: "r1", Mode: "unknown"},
		{RunID: "r1", Priority: &zero, Mode: "back"},
	} {
		if _, err := c.SetGroupPriority(t.Context(), "deploy", input); err == nil {
			t.Fatalf("accepted priority %+v", input)
		}
	}
}

func TestQueueManagerRejectsInvalidProjections(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"group":"other","capacity":1,"effective_capacity":1,"used_cost":0,"holders":[],"waiters":[]}`,
		`{"group":"prod","capacity":-1,"effective_capacity":1,"used_cost":0,"holders":[],"waiters":[]}`,
		`{"group":"prod","capacity":1,"effective_capacity":1,"used_cost":0,"holders":null,"waiters":[]}`,
		`{"group":"prod","capacity":1,"effective_capacity":1,"used_cost":0,"holders":[],"waiters":[{}]}`,
		`{"group":"prod","capacity":1,"effective_capacity":1,"used_cost":0,"holders":[],"waiters":[{"run_id":"r1","arrived_at":"2026-10-14T00:00:00Z","policy":"queue","position":0,"cost":1}]}`,
	} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if _, err := c.ReadQueueGroup(t.Context(), "prod"); err == nil {
			t.Fatalf("invalid group accepted: %s", body)
		}
	}
	for _, body := range []string{
		`{}`, `{"group":"other","run_id":"r1","found":true,"previous":0,"priority":1,"position":1}`,
		`{"group":"prod","run_id":"","found":true,"previous":0,"priority":1,"position":1}`,
		`{"group":"prod","run_id":"r1","found":true,"previous":0,"priority":1,"position":-1}`,
	} {
		c := testEngine(t, func(*http.Request) (*http.Response, error) { return engineResponse(200, body), nil })
		if _, err := c.SetGroupPriority(t.Context(), "prod", controllerwire.QueuePriorityWrite{RunID: "r1", Mode: "front", IdempotencyKey: "stable"}); err == nil {
			t.Fatalf("invalid priority accepted: %s", body)
		}
	}
}

func TestQueueManagerConstructionAndStructuredErrors(t *testing.T) {
	if _, err := NewQueueManager(EngineOptions{URL: "bad"}); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	failure := `{"error":{"code":"forbidden","message":"queue access refused","hint":"use a member credential"}}`
	manager, err := NewQueueManager(EngineOptions{URL: "https://controller.example", Bearer: func(_ context.Context) (string, error) { return "member-token", nil }, HTTP: &http.Client{Transport: engineTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer member-token" {
			t.Fatal("missing credential")
		}
		return engineResponse(403, failure), nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	manager.(*engineClient).decodeError = func(data []byte) (*apierr.Error, error) {
		var result apierr.Error
		err := json.Unmarshal(data, &result)
		return &result, err
	}
	if _, err := manager.ReadQueue(t.Context()); err == nil || !strings.Contains(err.Error(), "queue access refused") {
		t.Fatalf("failure lost: %v", err)
	}
}

func TestQueueManagerReplaysOnePriorityMutation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var bodies []string
		c := testEngine(t, func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("Idempotency-Key") != "stable" {
				t.Fatal("retry lost key")
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			bodies = append(bodies, string(body))
			if len(bodies) == 1 {
				resp := engineResponse(503, `{"error":{"code":"unavailable","message":"retry"}}`)
				resp.Header.Set("Retry-After", "0")
				return resp, nil
			}
			return engineResponse(200, `{"group":"prod","run_id":"r1","found":true,"previous":0,"priority":1,"position":1}`), nil
		})
		c.decodeError = func(data []byte) (*apierr.Error, error) {
			var failure apierr.Error
			err := json.Unmarshal(data, &failure)
			return &failure, err
		}
		_, err := c.SetGroupPriority(t.Context(), "prod", controllerwire.QueuePriorityWrite{RunID: "r1", Mode: "front", IdempotencyKey: "stable"})
		if err != nil || len(bodies) != 2 || bodies[0] != bodies[1] || bodies[0] != `{"run_id":"r1","mode":"front"}` {
			t.Fatalf("retry: %v %v", bodies, err)
		}
	})
}
