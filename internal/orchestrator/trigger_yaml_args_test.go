package orchestrator_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type triggerYAMLInputs struct {
	Region string `flag:"region" desc:"target region"`
}

var capturedTriggerRegion struct {
	mu sync.Mutex
	v  string
}

func setCapturedRegion(v string) {
	capturedTriggerRegion.mu.Lock()
	defer capturedTriggerRegion.mu.Unlock()
	capturedTriggerRegion.v = v
}

func capturedRegion() string {
	capturedTriggerRegion.mu.Lock()
	defer capturedTriggerRegion.mu.Unlock()
	return capturedTriggerRegion.v
}

type triggerYAMLPipe struct{ sparkwing.Base }

func (triggerYAMLPipe) Plan(_ context.Context, plan *sparkwing.Plan, in triggerYAMLInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(context.Context) error {
		setCapturedRegion(in.Region)
		return nil
	})
	return nil
}

func registerTriggerYAMLPipe(t *testing.T) {
	t.Helper()
	if _, ok := sparkwing.Lookup("trigger-yaml-pipe"); ok {
		return
	}
	sparkwing.Register[triggerYAMLInputs]("trigger-yaml-pipe",
		func() sparkwing.Pipeline[triggerYAMLInputs] { return triggerYAMLPipe{} })
}

type triggerRetryPipe struct{ sparkwing.Base }

func (triggerRetryPipe) Plan(_ context.Context, plan *sparkwing.Plan, in triggerYAMLInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(context.Context) error {
		setCapturedRegion(in.Region)
		return errors.New("deploy failed on purpose so the retry has work to do")
	})
	return nil
}

func registerTriggerRetryPipe(t *testing.T) {
	t.Helper()
	if _, ok := sparkwing.Lookup("trigger-retry-pipe"); ok {
		return
	}
	sparkwing.Register[triggerYAMLInputs]("trigger-retry-pipe",
		func() sparkwing.Pipeline[triggerYAMLInputs] { return triggerRetryPipe{} })
}

func triggerCheckout(t *testing.T, yaml string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sparkwing"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "sparkwing.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	prev := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(root)
	t.Cleanup(func() { sparkwing.SetWorkDir(prev) })
	return root
}

type triggerWorkerRig struct {
	st      *store.Store
	client  *client.Client
	backend orchestrator.Backends
	logs    *bytes.Buffer
	logger  *slog.Logger
}

func newTriggerWorkerRig(t *testing.T) *triggerWorkerRig {
	t.Helper()
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
	t.Cleanup(func() { _ = st.Close() })

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := orchestrator.NewControllerServer(t, st, quiet)
	t.Cleanup(srv.Close)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	c := client.NewWithToken(srv.URL, httpClient, "")
	buf := &bytes.Buffer{}
	return &triggerWorkerRig{
		st:      st,
		client:  c,
		backend: orchestrator.RemoteBackends(c, nil, nil, httpClient, store.DefaultConcurrencyLease),
		logs:    buf,
		logger:  slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

func (r *triggerWorkerRig) claim(t *testing.T, trig store.Trigger) *store.Trigger {
	t.Helper()
	ctx := context.Background()
	if trig.CreatedAt.IsZero() {
		trig.CreatedAt = time.Now()
	}
	if err := r.st.CreateTrigger(ctx, trig); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	claimed, err := r.st.ClaimSpecificTrigger(ctx, trig.ID, store.DefaultLeaseDuration)
	if err != nil {
		t.Fatalf("claim trigger: %v", err)
	}
	return claimed
}

func TestExecuteClaimedTrigger_MergesCheckoutYAMLArgs(t *testing.T) {
	registerTriggerYAMLPipe(t)
	setCapturedRegion("")
	triggerCheckout(t, `defaults:
  args:
    region: eu-west
pipelines:
  - name: trigger-yaml-pipe
    entrypoint: TriggerYAMLPipe
`)
	rig := newTriggerWorkerRig(t)
	trig := rig.claim(t, store.Trigger{
		ID: "trg-yaml-args", Pipeline: "trigger-yaml-pipe", TriggerSource: "dashboard",
	})

	orchestrator.ExecuteClaimedTrigger(context.Background(),
		orchestrator.WorkerOptions{Logger: rig.logger}, rig.backend, rig.client, trig)

	if got := capturedRegion(); got != "eu-west" {
		t.Fatalf("plan built with region=%q, want eu-west from defaults.args; log:\n%s", got, rig.logs.String())
	}

	run, err := rig.st.GetRun(context.Background(), trig.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Status != "success" {
		t.Fatalf("run status = %q, want success", run.Status)
	}

	if _, stored := run.Args["region"]; stored {
		t.Errorf("run row recorded a yaml-supplied arg: %v", run.Args)
	}
}

func TestExecuteClaimedTrigger_EvaluatesGuardsOnCheckoutValues(t *testing.T) {
	registerTriggerYAMLPipe(t)
	setCapturedRegion("")
	triggerCheckout(t, `defaults:
  args:
    region: prod
pipelines:
  - name: trigger-yaml-pipe
    entrypoint: TriggerYAMLPipe
    guards:
      reject:
        - arg:region=prod
`)
	rig := newTriggerWorkerRig(t)
	trig := rig.claim(t, store.Trigger{
		ID: "trg-yaml-guard", Pipeline: "trigger-yaml-pipe", TriggerSource: "dashboard",
	})

	orchestrator.ExecuteClaimedTrigger(context.Background(),
		orchestrator.WorkerOptions{Logger: rig.logger}, rig.backend, rig.client, trig)

	if got := capturedRegion(); got != "" {
		t.Fatalf("a guarded dispatch executed anyway (region=%q)", got)
	}
	if logged := rig.logs.String(); !strings.Contains(logged, "arg:region=prod") {
		t.Fatalf("guard did not reject the trigger; log:\n%s", logged)
	}
	if _, err := rig.st.GetRun(context.Background(), trig.ID); err == nil {
		t.Error("a rejected dispatch created a run row")
	}
}

func TestExecuteClaimedTrigger_RetryReproducesRecordedRevisionArgs(t *testing.T) {
	registerTriggerRetryPipe(t)
	rig := newTriggerWorkerRig(t)
	ctx := context.Background()

	const recordedRevisionConfig = `defaults:
  args:
    region: eu-west
pipelines:
  - name: trigger-retry-pipe
    entrypoint: TriggerRetryPipe
`

	setCapturedRegion("")
	triggerCheckout(t, recordedRevisionConfig)
	original := rig.claim(t, store.Trigger{
		ID: "trg-retry-source", Pipeline: "trigger-retry-pipe", TriggerSource: "dashboard",
	})
	orchestrator.ExecuteClaimedTrigger(ctx,
		orchestrator.WorkerOptions{Logger: rig.logger}, rig.backend, rig.client, original)
	if got := capturedRegion(); got != "eu-west" {
		t.Fatalf("original ran with region=%q, want eu-west", got)
	}
	if _, stored := mustRun(t, rig.st, original.ID).Args["region"]; stored {
		t.Fatalf("fixture is not exercising the case: the region must come from yaml, not the run row")
	}

	setCapturedRegion("")
	triggerCheckout(t, recordedRevisionConfig)
	retry := rig.claim(t, store.Trigger{
		ID: "trg-retry", Pipeline: "trigger-retry-pipe", TriggerSource: "dashboard",
		RetryOf: original.ID, RetrySource: "manual",
	})
	orchestrator.ExecuteClaimedTrigger(ctx,
		orchestrator.WorkerOptions{Logger: rig.logger}, rig.backend, rig.client, retry)

	if got := capturedRegion(); got != "eu-west" {
		t.Fatalf("retry ran with region=%q, want the original's eu-west; log:\n%s", got, rig.logs.String())
	}
}

func mustRun(t *testing.T, st *store.Store, runID string) *store.Run {
	t.Helper()
	run, err := st.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("get run %s: %v", runID, err)
	}
	return run
}
