package acceptance_test

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestSurfaceQueueTransport(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		var calls []string
		manager, err := client.NewQueueManager(client.EngineOptions{URL: endpoint, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
			calls = append(calls, req.Method+" "+req.URL.RequestURI())
			body := `{}`
			if req.Method == "GET" && req.URL.Path != "/api/v1/queue" {
				body = `{"group":"g:deploy","capacity":2,"effective_capacity":1,"used_cost":2,"holders":[],"waiters":[]}`
			}
			if req.Method == "PUT" {
				var write controllerwire.QueuePriorityWrite
				if err := json.NewDecoder(req.Body).Decode(&write); err != nil {
					t.Fatal(err)
				}
				if write.RunID != "412" || write.Priority == nil || *write.Priority != 0 || write.Mode != "" || req.Header.Get("Idempotency-Key") != "one-mutation" {
					t.Fatalf("priority lost: %+v %v", write, req.Header)
				}
				body = `{"group":"g:deploy","run_id":"r412","found":false,"previous":0,"priority":0,"position":0}`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.ReadQueue(t.Context()); err != nil {
			t.Fatal(err)
		}
		group, err := manager.ReadQueueGroup(t.Context(), "g:deploy")
		if err != nil || group.EffectiveCapacity != 1 || group.UsedCost != 2 {
			t.Fatalf("projection: %+v %v", group, err)
		}
		zero := 0
		result, err := manager.SetGroupPriority(t.Context(), "g:deploy", controllerwire.QueuePriorityWrite{RunID: "412", Priority: &zero, IdempotencyKey: "one-mutation"})
		if err != nil || result.RunID != "r412" || result.Found {
			t.Fatalf("missing result: %+v %v", result, err)
		}
		want := []string{"GET /api/v1/queue", "GET /api/v1/queue/groups/g:deploy", "PUT /api/v1/queue/groups/g:deploy/priority"}
		if !reflect.DeepEqual(calls, want) {
			t.Fatalf("routes: %v", calls)
		}
	}
}

func TestSurfaceQueueGrammar(t *testing.T) {
	for _, args := range [][]string{
		{"--profile", "prod", "queue", "list", "--group", "g:deploy"},
		{"--profile", "prod", "queue", "priority", "--group", "g:deploy", "--run", "412", "--set", "0"},
	} {
		inv, err := clisurface.Parse("sparkwing", args)
		if err != nil || inv.Value("profile") != "prod" || inv.Value("group") != "g:deploy" {
			t.Fatalf("grammar: %+v %v", inv, err)
		}
	}
	for _, args := range [][]string{{"queue", "priority", "--run", "412"}, {"queue", "list", "--namespace", "g:deploy"}, {"cluster", "concurrency"}} {
		if _, err := clisurface.Parse("sparkwing", args); err == nil {
			t.Fatalf("invalid queue command accepted: %v", args)
		}
	}
}
