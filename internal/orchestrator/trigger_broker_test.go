package orchestrator_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type brokeredTriggerPipe struct{ sparkwing.Base }

func (brokeredTriggerPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", func(ctx context.Context) error {
		sparkwing.Info(ctx, "building")
		return nil
	})
	sparkwing.Job(plan, "test", func(context.Context) error { return nil }).Needs(build)
	return nil
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A claimed trigger's handler is the team's own code. Through the broker it
// plans and runs its whole run holding only the broker's capability.
func TestTriggerBrokerCarriesAClaimedRunWithoutTheRunnerToken(t *testing.T) {
	if _, ok := sparkwing.Lookup("brokered-trigger-pipe"); !ok {
		sparkwing.Register[sparkwing.NoInputs]("brokered-trigger-pipe",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return brokeredTriggerPipe{} })
	}
	rig := newTriggerWorkerRig(t)
	trig := rig.claim(t, store.Trigger{ID: "trg-brokered", Pipeline: "brokered-trigger-pipe", TriggerSource: "dashboard"})

	var refusals lockedBuffer
	broker, err := orchestrator.StartTriggerBroker(rig.client.BaseURL(), "", "runner-token", trig,
		slog.New(slog.NewTextHandler(&refusals, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close(context.Background())
	httpClient := &http.Client{Timeout: 30 * time.Second}
	child := client.NewWithToken(broker.URL(), httpClient, broker.Capability())
	orchestrator.ExecuteClaimedTrigger(context.Background(),
		orchestrator.WorkerOptions{Logger: rig.logger, ControllerURL: broker.URL(), Token: broker.Capability()},
		orchestrator.RemoteBackends(child, nil, nil, httpClient, store.DefaultConcurrencyLease), child, trig)

	if run := mustRun(t, rig.st, trig.ID); run.Status != "success" {
		t.Fatalf("brokered run status = %q, want success; log:\n%s", run.Status, rig.logs.String())
	}
	if got := refusals.String(); strings.Contains(got, "refused") {
		t.Fatalf("the broker refused a route the handler needed:\n%s", got)
	}
}

func TestTriggerBrokerAdmitsOnlyItsRunWithTheRunnerToken(t *testing.T) {
	var mu sync.Mutex
	var forwarded []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		forwarded = append(forwarded, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"run_id":"child-run"}`))
	}))
	defer upstream.Close()
	broker, err := orchestrator.StartTriggerBroker(upstream.URL, "", "runner-token",
		&store.Trigger{ID: "run-a", Pipeline: "build"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close(context.Background())
	send := func(method, path, auth, body string) int {
		t.Helper()
		req, err := http.NewRequest(method, broker.URL()+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	capability := broker.Capability()
	for _, tc := range []struct {
		method, path, auth, body string
		want                     int
	}{
		{"GET", "/api/v1/runs/run-a", "", "", http.StatusUnauthorized},
		{"GET", "/api/v1/runs/run-a", "runner-token", "", http.StatusUnauthorized},
		{"GET", "/api/v1/runs/run-b", capability, "", http.StatusForbidden},
		{"GET", "/api/v1/runs/run-b/nodes/build/output", capability, "", http.StatusForbidden},
		{"GET", "/api/v1/pipelines/build/latest", capability, "", http.StatusForbidden},
		{"GET", "/api/v1/pipelines/other/profile", capability, "", http.StatusForbidden},
		{"GET", "/api/v1/triggers/spawned-child?parent_run_id=run-b", capability, "", http.StatusForbidden},
		{"POST", "/api/v1/runs/run-b/cancel", capability, "", http.StatusForbidden},
		{"POST", "/api/v1/nodes/claim", capability, `{}`, http.StatusForbidden},
		{"POST", "/api/v1/triggers/claim", capability, `{}`, http.StatusForbidden},
		{"GET", "/api/v1/secrets/DEPLOY_KEY?run=run-b", capability, "", http.StatusForbidden},
		{"POST", "/api/v1/triggers", capability, `{"pipeline":"x","parent_run_id":"run-b"}`, http.StatusForbidden},
		{"POST", "/api/v1/runs", capability, `{"id":"run-b"}`, http.StatusForbidden},
		{"POST", "/api/v1/concurrency/deploy/release", capability, `{"holder_id":"run-b/n1"}`, http.StatusForbidden},
		{"GET", "/api/v1/runs/run-a/nodes", capability, "", http.StatusOK},
		{"POST", "/api/v1/runs", capability, `{"id":"run-a"}`, http.StatusOK},
		{"GET", "/api/v1/secrets/DEPLOY_KEY?run=run-a", capability, "", http.StatusOK},
		{"POST", "/api/v1/concurrency/deploy/release", capability, `{"holder_id":"run-a/n1"}`, http.StatusOK},
		{"POST", "/api/v1/triggers", capability, `{"pipeline":"x","parent_run_id":"run-a"}`, http.StatusOK},
		{"GET", "/api/v1/runs/child-run", capability, "", http.StatusOK},
		{"GET", "/api/v1/runs/child-run/nodes/build/output", capability, "", http.StatusOK},
		{"GET", "/api/v1/pipelines/build/profile", capability, "", http.StatusOK},
	} {
		if got := send(tc.method, tc.path, tc.auth, tc.body); got != tc.want {
			t.Errorf("%s %s with %q = %d, want %d", tc.method, tc.path, tc.auth, got, tc.want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, line := range forwarded {
		if !strings.HasSuffix(line, "Bearer runner-token") || strings.Contains(line, "run-b") {
			t.Errorf("upstream saw %q, want only run-a routes under the runner token", line)
		}
	}
}
