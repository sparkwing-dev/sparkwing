package orchestrator_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type remoteOKPipe struct{ sparkwing.Base }

func (remoteOKPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error { return nil })
	return nil
}

var remoteRegisterOnce sync.Once

func registerRemotePipelines(t *testing.T) {
	t.Helper()
	remoteRegisterOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("remote-ok",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return &remoteOKPipe{} })
	})
}

func TestRunLocal_RemoteBackends_DispatchesAgainstController(t *testing.T) {
	registerRemotePipelines(t)

	ctrlDB := filepath.Join(t.TempDir(), "controller.db")
	ctrlStore, err := store.Open(ctrlDB)
	if err != nil {
		t.Fatalf("controller store: %v", err)
	}
	t.Cleanup(func() { _ = ctrlStore.Close() })

	srv := orchestrator.NewControllerServer(t, ctrlStore, nil)
	t.Cleanup(srv.Close)

	c := client.NewWithToken(srv.URL, nil, "")
	if c.BaseURL() != srv.URL {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL(), srv.URL)
	}

	paths := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
		Pipeline: "remote-ok",
		State:    c,
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}

	run, err := ctrlStore.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("controller-side GetRun: %v", err)
	}
	if run.Status != "success" {
		t.Errorf("controller-side run.Status = %q, want success", run.Status)
	}
	if run.Pipeline != "remote-ok" {
		t.Errorf("controller-side run.Pipeline = %q, want remote-ok", run.Pipeline)
	}
}

func TestRemoteBackends_FromBaseURL(t *testing.T) {
	c := client.NewWithToken("https://controller.example", nil, "tok-abc")
	art := &noListArtifact{}
	b := orchestrator.RemoteBackends(c, nil, art, nil, 0)
	if b.State == nil || b.Logs == nil || b.Concurrency == nil {
		t.Fatalf("RemoteBackends = %+v", b)
	}
	if b.Artifact != art {
		t.Errorf("Artifact = %v, want the supplied store", b.Artifact)
	}
	if c.Token() != "tok-abc" {
		t.Errorf("Token() = %q, want tok-abc", c.Token())
	}
}

func TestRemoteBackends_NilArtifact(t *testing.T) {
	c := client.NewWithToken("https://controller.example", nil, "")
	b := orchestrator.RemoteBackends(c, nil, nil, nil, 0)
	if b.Artifact != nil {
		t.Errorf("Artifact = %v, want nil", b.Artifact)
	}
}
