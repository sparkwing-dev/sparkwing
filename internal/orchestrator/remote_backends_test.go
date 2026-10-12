package orchestrator_test

import (
	"context"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
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

func TestRemoteBackends_FromBaseURL(t *testing.T) {
	c := client.NewWithToken("https://controller.example", nil, "tok-abc")
	art := &noListArtifact{}
	b := orchestrator.RemoteBackends(c, testLogBackend(t), art, nil, 0)
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
	b := orchestrator.RemoteBackends(c, testLogBackend(t), nil, nil, 0)
	if b.Artifact != nil {
		t.Errorf("Artifact = %v, want nil", b.Artifact)
	}
}

func testLogBackend(t *testing.T) orchestrator.LogBackend {
	t.Helper()
	logs, err := fs.NewLogStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return orchestrator.NewLogStoreBackend(logs, nil)
}

type noListArtifact struct {
	storage.ArtifactStore
}

func (n *noListArtifact) List(_ context.Context, _ string) ([]string, error) {
	return nil, storage.ErrListNotSupported
}
