package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestClaimedUnknownPipelineFailsVisibly(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var controllerLogs bytes.Buffer
	srv := orchestrator.NewControllerServer(t, st, slog.New(slog.NewTextHandler(&controllerLogs, nil)))
	t.Cleanup(srv.Close)
	cli := client.New(srv.URL, nil)
	const id = "unknown-pipeline-trigger"
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: id, Pipeline: "build-deploy", Repo: "acme/web", GitSHA: strings.Repeat("a", 40),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	trig, err := st.ClaimNextTrigger(ctx, 0)
	if err != nil || trig == nil {
		t.Fatalf("claim trigger: %v, %+v", err, trig)
	}
	var logs bytes.Buffer
	orchestrator.ExecuteClaimedTrigger(ctx, orchestrator.WorkerOptions{
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	}, orchestrator.RemoteBackends(cli, nil, nil, nil, 0), cli, trig)

	got, err := cli.GetTrigger(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" {
		t.Errorf("trigger status = %q, want failed", got.Status)
	}
	if !strings.Contains(got.Error, "pipeline build-deploy is not defined") {
		t.Errorf("trigger API error = %q", got.Error)
	}
	failed, err := cli.ListTriggers(ctx, store.TriggerFilter{Statuses: []string{"failed"}})
	if err != nil || len(failed) != 1 || failed[0].ID != id || failed[0].Error != got.Error {
		t.Errorf("failed trigger listing = %+v, err %v", failed, err)
	}
	run, err := cli.GetRun(ctx, id)
	if err != nil {
		t.Fatalf("failed run is absent from API: %v", err)
	}
	if run.Status != "failed" {
		t.Errorf("run status = %q, want failed", run.Status)
	}
	for _, part := range []string{"pipeline build-deploy is not defined in acme/web@" + strings.Repeat("a", 40), "defined:"} {
		if !strings.Contains(run.Error, part) {
			t.Errorf("run error = %q, want %q", run.Error, part)
		}
	}
	if !strings.Contains(controllerLogs.String(), "level=WARN") || !strings.Contains(controllerLogs.String(), "build-deploy") {
		t.Errorf("controller warning absent: %s", controllerLogs.String())
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("worker warning absent: %s", logs.String())
	}
}

func TestClaimedDefinedPipelineSetupFailureFailsPendingRun(t *testing.T) {
	registerRemotePipelines(t)
	t.Setenv(orchestrator.StoreWedgeBudgetEnvVar, "secret-should-not-be-shown")
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := orchestrator.NewControllerServer(t, st, nil)
	t.Cleanup(srv.Close)
	cli := client.New(srv.URL, nil)
	const id = "defined-setup-failure"
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: id, Pipeline: "remote-ok", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{
		ID: id, Pipeline: "remote-ok", Status: "pending", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	trigger, err := st.ClaimNextTrigger(ctx, 0)
	if err != nil || trigger == nil {
		t.Fatalf("claim trigger: %v, %+v", err, trigger)
	}
	orchestrator.ExecuteClaimedTrigger(ctx, orchestrator.WorkerOptions{},
		orchestrator.RemoteBackends(cli, nil, nil, nil, 0), cli, trigger)

	run, err := cli.GetRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || !strings.Contains(run.Error, "pipeline setup failed before dispatch") ||
		strings.Contains(run.Error, "secret-should-not-be-shown") {
		t.Fatalf("setup failure run = status %q, error %q", run.Status, run.Error)
	}
	gotTrigger, err := cli.GetTrigger(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if gotTrigger.Status != "failed" {
		t.Fatalf("setup failure trigger status = %q, want failed", gotTrigger.Status)
	}
}

func TestClaimedSetupFailureKeepsTriggerOpenWhenRunWriteUnavailable(t *testing.T) {
	registerRemotePipelines(t)
	t.Setenv(orchestrator.StoreWedgeBudgetEnvVar, "invalid-duration")
	var runRead, triggerDone atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runs/setup-write-unavailable":
			runRead.Store(true)
			http.Error(w, "state unavailable", http.StatusServiceUnavailable)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/setup-write-unavailable/done":
			triggerDone.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cli := client.New(srv.URL, nil)
	orchestrator.ExecuteClaimedTrigger(context.Background(), orchestrator.WorkerOptions{},
		orchestrator.RemoteBackends(cli, nil, nil, nil, 0), cli,
		&store.Trigger{ID: "setup-write-unavailable", Pipeline: "remote-ok"})
	if !runRead.Load() || triggerDone.Load() {
		t.Fatalf("run read attempted = %t, trigger marked done = %t", runRead.Load(), triggerDone.Load())
	}
}

func TestClaimedSetupFailureConfirmsAmbiguousRunWrite(t *testing.T) {
	registerRemotePipelines(t)
	t.Setenv(orchestrator.StoreWedgeBudgetEnvVar, "invalid-duration")
	for _, tc := range []struct {
		name       string
		finalState string
		wantDone   bool
	}{
		{name: "write rejected", finalState: "pending", wantDone: false},
		{name: "response lost after commit", finalState: "failed", wantDone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads, writes atomic.Int32
			var done atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runs/ambiguous-setup":
					status := "pending"
					if reads.Add(1) > 1 {
						status = tc.finalState
					}
					_ = json.NewEncoder(w).Encode(store.Run{ID: "ambiguous-setup", Status: status})
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runs/ambiguous-setup/finish":
					writes.Add(1)
					http.Error(w, "response unavailable", http.StatusServiceUnavailable)
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/ambiguous-setup/done":
					done.Store(true)
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			cli := client.New(srv.URL, nil)
			orchestrator.ExecuteClaimedTrigger(context.Background(), orchestrator.WorkerOptions{},
				orchestrator.RemoteBackends(cli, nil, nil, nil, 0), cli,
				&store.Trigger{ID: "ambiguous-setup", Pipeline: "remote-ok"})
			if reads.Load() != 2 || writes.Load() != 1 || done.Load() != tc.wantDone {
				t.Fatalf("run reads = %d, writes = %d, trigger done = %t; want 2, 1, %t",
					reads.Load(), writes.Load(), done.Load(), tc.wantDone)
			}
		})
	}
}

func TestClaimedDefinedPipelineStillCompletes(t *testing.T) {
	registerRemotePipelines(t)
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := orchestrator.NewControllerServer(t, st, nil)
	t.Cleanup(srv.Close)
	cli := client.New(srv.URL, nil)
	const id = "defined-pipeline-trigger"
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: id, Pipeline: "remote-ok", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	trig, err := st.ClaimNextTrigger(ctx, 0)
	if err != nil || trig == nil {
		t.Fatalf("claim trigger: %v, %+v", err, trig)
	}
	orchestrator.ExecuteClaimedTrigger(ctx, orchestrator.WorkerOptions{},
		orchestrator.RemoteBackends(cli, nil, nil, nil, 0), cli, trig)
	got, err := cli.GetTrigger(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "done" {
		t.Errorf("trigger status = %q, want done", got.Status)
	}
	run, err := cli.GetRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "success" {
		t.Errorf("run status = %q, want success", run.Status)
	}
}
