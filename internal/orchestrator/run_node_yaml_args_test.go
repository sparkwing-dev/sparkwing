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

type podYAMLInputs struct {
	Region string `flag:"region" desc:"target region"`
	Token  string `flag:"token" desc:"deploy token" secret:"true"`
}

type podYAMLPipe struct{ sparkwing.Base }

func (podYAMLPipe) Plan(_ context.Context, plan *sparkwing.Plan, in podYAMLInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		sparkwing.Info(ctx, "region=%s token=%s done", in.Region, in.Token)
		return nil
	})
	return nil
}

func registerPodYAMLPipe(t *testing.T) {
	t.Helper()
	if _, ok := sparkwing.Lookup("pod-yaml-pipe"); ok {
		return
	}
	sparkwing.Register[podYAMLInputs]("pod-yaml-pipe",
		func() sparkwing.Pipeline[podYAMLInputs] { return podYAMLPipe{} })
}

func writeCheckout(t *testing.T, yaml string) string {
	t.Helper()
	root := isolateCheckout(t)
	if err := os.MkdirAll(filepath.Join(root, ".sparkwing"), 0o755); err != nil {
		t.Fatalf("mkdir .sparkwing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write sparkwing.yaml: %v", err)
	}
	return root
}

func isolateCheckout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	prev := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })
	return root
}

func TestRunNodeOnce_MergesCheckoutYAMLArgs(t *testing.T) {
	registerPodYAMLPipe(t)
	isolateProfiles(t)

	const secretValue = "yaml-supplied-supersecret"
	writeCheckout(t, `defaults:
  args:
    region: eu-west
pipelines:
  - name: pod-yaml-pipe
    entrypoint: PodYAMLPipe
    args:
      token: `+secretValue+`
`)

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
		runID  = "run-pod-yaml"
		nodeID = "deploy"
	)

	if err := st.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "pod-yaml-pipe",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	res, err := orchestrator.RunNodeOnce(ctx, srv.URL, "", runID, nodeID,
		"pod:"+runID+":"+nodeID, "", &captureLogger{}, quiet, nil)
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
	log := string(body)
	if !strings.Contains(log, "region=eu-west") {
		t.Errorf("defaults.args value never reached the pod's plan:\n%s", log)
	}
	if strings.Contains(log, secretValue) {
		t.Errorf("pipeline entry's secret arg was not registered with the pod masker:\n%s", log)
	}
	if !strings.Contains(log, "token=*** done") {
		t.Errorf("pipeline entry's args: value never reached the pod's plan (masked form absent):\n%s", log)
	}
}

func TestRunNodeOnce_StoredArgsBeatCheckoutYAML(t *testing.T) {
	registerPodYAMLPipe(t)
	isolateProfiles(t)

	writeCheckout(t, `defaults:
  args:
    region: eu-west
pipelines:
  - name: pod-yaml-pipe
    entrypoint: PodYAMLPipe
`)

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
		runID  = "run-pod-yaml-override"
		nodeID = "deploy"
	)
	if err := st.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "pod-yaml-pipe",
		Status:    "running",
		StartedAt: time.Now(),
		Args:      map[string]string{"region": "us-east"},
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	res, err := orchestrator.RunNodeOnce(ctx, srv.URL, "", runID, nodeID,
		"pod:"+runID+":"+nodeID, "", &captureLogger{}, quiet, nil)
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
	if !strings.Contains(string(body), "region=us-east") {
		t.Errorf("stored arg lost to the project default:\n%s", body)
	}
}
