package orchestrator_test

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
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
