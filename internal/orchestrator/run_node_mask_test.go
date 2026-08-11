package orchestrator_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type podMaskInputs struct {
	Token string `flag:"token" desc:"deploy token" secret:"true"`
}

type podMaskPipe struct{ sparkwing.Base }

func (podMaskPipe) Plan(_ context.Context, plan *sparkwing.Plan, in podMaskInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "leak", func(ctx context.Context) error {
		sparkwing.Info(ctx, "deploying with token=%s now", in.Token)
		return nil
	})
	return nil
}

func registerPodMaskPipe(t *testing.T) {
	t.Helper()
	if _, ok := sparkwing.Lookup("pod-mask-pipe"); ok {
		return
	}
	sparkwing.Register[podMaskInputs]("pod-mask-pipe",
		func() sparkwing.Pipeline[podMaskInputs] { return podMaskPipe{} })
}

// TestRunNodeOnce_MasksSecretsInNodeLog pins the cluster/pod execution
// path to the same redaction guarantee the local path already has: the
// per-run masker built from the pipeline's secret args must reach the
// node log wrapper, so a job that logs a secret value persists `***`
// and never the plaintext. The masker is installed on the context, and
// InProcessRunner reads it back from there; dropping that installation
// silently disables masking for every remote node.
func TestRunNodeOnce_MasksSecretsInNodeLog(t *testing.T) {
	registerPodMaskPipe(t)
	isolateProfiles(t)

	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	p := orchestrator.PathsAt(home)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).Handler())
	defer srv.Close()

	ctx := context.Background()
	const (
		secretValue = "pod-side-supersecret"
		runID       = "run-pod-mask"
		nodeID      = "leak"
	)
	if err := st.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "pod-mask-pipe",
		Status:    "running",
		StartedAt: time.Now(),
		Args:      map[string]string{"token": secretValue},
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	cap := &captureLogger{}
	res, err := orchestrator.RunNodeOnce(ctx, srv.URL, "", runID, nodeID,
		"pod:"+runID+":"+nodeID, "", cap, quiet, nil)
	if err != nil {
		t.Fatalf("RunNodeOnce: %v", err)
	}
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %q (err=%v), want success", res.Outcome, res.Err)
	}

	body, err := os.ReadFile(p.NodeLog(runID, nodeID))
	if err != nil {
		t.Fatalf("read node log: %v", err)
	}
	if strings.Contains(string(body), secretValue) {
		t.Fatalf("persisted node log leaks the raw secret value:\n%s", body)
	}
	if !strings.Contains(string(body), "token=*** now") {
		t.Fatalf("persisted node log missing the masked line:\n%s", body)
	}
	for _, rec := range cap.Snapshot() {
		if strings.Contains(rec.Msg, secretValue) {
			t.Fatalf("delegate received a record with the raw secret value: %+v", rec)
		}
	}
}
