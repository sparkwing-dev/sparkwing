package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
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
}

func TestQueueManagerRejectsInvalidInputsBeforeHTTP(t *testing.T) {
	c := testEngine(t, func(*http.Request) (*http.Response, error) { t.Fatal("invalid input reached HTTP"); return nil, io.EOF })
	for _, group := range []string{"", " ", ".", "..", "bad\nname"} {
		if _, err := c.ReadQueueGroup(t.Context(), group); err == nil {
			t.Fatalf("accepted group %q", group)
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
	if _, err := manager.ReadQueue(t.Context()); err == nil || !strings.Contains(err.Error(), "queue access refused") {
		t.Fatalf("failure lost: %v", err)
	}
}
