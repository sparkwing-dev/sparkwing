//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineenv"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const fixtureToken = "hunter2-fixture-token"

type fixtureResolver struct{}

func (fixtureResolver) Resolve(_ context.Context, name string) (string, bool, error) {
	if name == "DEPLOY_TOKEN" {
		return fixtureToken, true, nil
	}
	return "", false, secretsource.ErrMissing
}

type memLogs struct {
	mu      sync.Mutex
	records map[string][]sparkwing.LogRecord
	open    int
}

type memLog struct {
	logs *memLogs
	node string
}

func (l memLog) Emit(rec sparkwing.LogRecord) {
	l.logs.mu.Lock()
	defer l.logs.mu.Unlock()
	l.logs.records[l.node] = append(l.logs.records[l.node], rec)
}

func (l memLog) Close() error {
	l.logs.mu.Lock()
	defer l.logs.mu.Unlock()
	l.logs.open--
	return nil
}

func (m *memLogs) text(node string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for _, r := range m.records[node] {
		b.WriteString(r.Msg + "\n")
	}
	return b.String()
}

// safety: masks each line when it arrives, as a real plan sink does, so a
// value registered too late shows in the text.
type planSinkText struct {
	masker *secrets.Masker
	mu     sync.Mutex
	text   strings.Builder
}

func (p *planSinkText) Text(_, line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.text.WriteString(p.masker.Mask(line) + "\n")
}

func (p *planSinkText) Record(rec nodeproto.FrameLog) { p.Text("", rec.Message) }

func (p *planSinkText) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.text.String()
}

type attemptHarness struct {
	st     *store.Store
	logs   *memLogs
	plan   *planSinkText
	marker string
	usage  map[string]Usage
	mu     sync.Mutex
}

func (h *attemptHarness) row(t *testing.T, runID, node string) *store.Node {
	t.Helper()
	row, err := h.st.GetNode(t.Context(), runID, node)
	if err != nil {
		t.Fatalf("node %s: %v", node, err)
	}
	return row
}

func TestAttemptExecutor(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a pipeline binary; run without -short")
	}
	bin := filepath.Join(t.TempDir(), "attemptfixture")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/attemptfixture").CombinedOutput(); err != nil {
		t.Fatalf("build the fixture: %v\n%s", err, out)
	}
	host := func(t *testing.T, runID string, args map[string]string, env []string) (*attemptHarness, RunResult) {
		t.Helper()
		srv, err := Start(nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = srv.Close() })
		h := &attemptHarness{st: openStore(t), logs: &memLogs{records: map[string][]sparkwing.LogRecord{}}, plan: &planSinkText{masker: secrets.NewMasker()}, marker: t.TempDir(), usage: map[string]Usage{}}
		args["marker"] = h.marker
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		res, err := Execute(ctx, Config{
			Session: SessionConfig{
				Entry: pipelineentry.Binary(bin), Dir: t.TempDir(), Grace: 2 * time.Second,
				Env: env, Sink: h.plan, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
			},
			Pipeline: "attempts", Args: args,
			Run:   nodeproto.RunContext{RunID: runID},
			State: h.st, Logger: slog.New(slog.DiscardHandler), Heartbeat: time.Hour,
			Attempts: &Attempts{
				Routes: srv, Resolver: fixtureResolver{}, Masker: h.plan.masker,
				OpenLog: func(_ context.Context, _, node string) (NodeLog, error) {
					h.logs.mu.Lock()
					defer h.logs.mu.Unlock()
					h.logs.open++
					return memLog{logs: h.logs, node: node}, nil
				},
				Usage: func(_ context.Context, n *NodeRun, u Usage) {
					h.mu.Lock()
					defer h.mu.Unlock()
					h.usage[n.Plan.ID] = u
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if h.logs.open != 0 {
			t.Errorf("%d node logs left open", h.logs.open)
		}
		return h, res
	}

	t.Run("RunsANodeEndToEnd", func(t *testing.T) {
		// safety: a launcher Job's host names its runner but no labels, so neither the caller's nor the plan's may stand in.
		t.Setenv("SPARKWING_RUNNER_NAME", "pool-a")
		t.Setenv("SPARKWING_RUNNER_TYPE", "kubernetes")
		t.Setenv("SPARKWING_RUNNER_LABELS", "")
		// safety: the caller's environment as a hosted run passes it, so a test run inside a node does not hand the
		// plan process that node's SPARKWING_NODE_URL without its bearer.
		env := append(engineenv.Strip(os.Environ()), "SPARKWING_RUNNER_LABELS=local,caller", "SPARKWING_AGENT_TOKEN=engine", "SPARKWING_GATE_INDEX=/repo/.git/gate-index",
			"SPARKWING_TEST_PG_URL=postgres://caller", "AWS_SECRET_ACCESS_KEY=caller-aws")
		h, res := host(t, "run-attempt", map[string]string{}, env)
		if res.Status != "success" {
			t.Fatalf("status=%s err=%v, want success", res.Status, res.Err)
		}
		row := h.row(t, "run-attempt", "build")
		if row.Status != "done" || row.Outcome != string(sparkwing.Success) {
			t.Errorf("build row status=%q outcome=%q error=%q", row.Status, row.Outcome, row.Error)
		}
		raw, err := h.st.GetNodeOutput(t.Context(), "run-attempt", "build")
		if err != nil {
			t.Fatal(err)
		}
		var out struct{ Env, Run, Leak, Gate, PG, AWS, Labels string }
		if err := json.Unmarshal(raw, &out); err != nil || out.Env != "from-plan" || out.Run != "run-attempt" || out.Leak != "" || out.AWS != "caller-aws" {
			t.Errorf("build output = %s (err %v), want the node env, run id and caller's env and no engine SPARKWING_ variable", raw, err)
		}
		if out.Labels != "" {
			t.Errorf("the node saw SPARKWING_RUNNER_LABELS=%q on a host that advertises none", out.Labels)
		}
		if out.PG != "postgres://caller" {
			t.Errorf("the node saw SPARKWING_TEST_PG_URL=%q; a caller's own SPARKWING_* setting must reach pipeline code", out.PG)
		}
		if out.Gate != "/repo/.git/gate-index" {
			t.Errorf("the node saw SPARKWING_GATE_INDEX=%q; a hook's staged-diff step reads the working tree instead of the commit", out.Gate)
		}
		logs := h.logs.text("build")
		if strings.Contains(logs, fixtureToken) {
			t.Errorf("the node log holds the secret unmasked:\n%s", logs)
		}
		for _, want := range []string{"stdout token=", "record token=", "cleanup sh", "cleaned "} {
			if !strings.Contains(logs, want) {
				t.Errorf("node log lacks %q:\n%s", want, logs)
			}
		}
		if got, err := os.ReadFile(filepath.Join(h.marker, "build")); err != nil || string(got) != "caller-aws\n" {
			t.Errorf("cleanup wrote %q (err %v), want the caller's environment", got, err)
		}
		if u, ok := h.usage["build"]; !ok || u.Wall <= 0 {
			t.Errorf("usage hook got %+v (called %v)", u, ok)
		}
		run, err := h.st.GetRun(t.Context(), "run-attempt")
		if err != nil {
			t.Fatal(err)
		}
		var snap nodeproto.PlanDoc
		if err := json.Unmarshal(run.PlanSnapshot, &snap); err != nil || len(snap.Jobs) != 1 || snap.Jobs[0].ID != "build" {
			t.Errorf("plan snapshot = %s (err %v), want the declared job", run.PlanSnapshot, err)
		}
	})

	t.Run("TheEngineEnvironmentStaysOutByDefault", func(t *testing.T) {
		t.Setenv("AWS_SECRET_ACCESS_KEY", "host-aws")
		h, res := host(t, "run-minimal", map[string]string{}, nil)
		if res.Status != "success" {
			t.Fatalf("status=%s err=%v, want success", res.Status, res.Err)
		}
		raw, err := h.st.GetNodeOutput(t.Context(), "run-minimal", "build")
		if err != nil || strings.Contains(string(raw), "host-aws") {
			t.Errorf("build output = %s (err %v), want no engine credential", raw, err)
		}
		if got, err := os.ReadFile(filepath.Join(h.marker, "build")); err != nil || string(got) != "\n" {
			t.Errorf("cleanup wrote %q (err %v), want no engine credential", got, err)
		}
	})

	t.Run("AFailureMessageIsMasked", func(t *testing.T) {
		h, res := host(t, "run-leak", map[string]string{"leak": "true"}, nil)
		if res.Status != "failed" {
			t.Fatalf("status=%s err=%v, want failed", res.Status, res.Err)
		}
		row := h.row(t, "run-leak", "leak")
		if !strings.Contains(row.Error, "deploy refused token") || strings.Contains(row.Error, fixtureToken) {
			t.Errorf("leak row error = %q, want the message with the secret masked", row.Error)
		}
	})

	t.Run("NoStoredRecordHoldsASecret", func(t *testing.T) {
		const pass = "pass-fixture-secret"
		env := append(engineenv.Minimal(), "SPARKWING_HOME="+t.TempDir())
		h, res := host(t, "run-exec", map[string]string{"exec": "true", "pass": pass}, env)
		if res.Status != "failed" {
			t.Fatalf("status=%s err=%v, want failed", res.Status, res.Err)
		}
		ledger, err := os.ReadFile(filepath.Join(h.marker, "ledger.json"))
		if err != nil || !strings.Contains(string(ledger), "sh -c") {
			t.Fatalf("session ledger record = %q (err %v), want the step command's record", ledger, err)
		}
		row := h.row(t, "run-exec", "exec")
		steps, err := h.st.ListNodeSteps(t.Context(), "run-exec")
		if err != nil {
			t.Fatal(err)
		}
		stored := map[string]string{
			"session ledger": string(ledger),
			"node log":       h.logs.text("exec"),
			"node error":     row.Error,
			"annotations":    strings.Join(row.Annotations, "\n"),
			"summary":        row.Summary,
		}
		for _, s := range steps {
			stored["step "+s.StepID] = s.Summary + "\n" + strings.Join(s.Annotations, "\n")
		}
		if !strings.Contains(stored["node log"], "exec ") || !strings.Contains(row.Error, "exec ran with token") {
			t.Errorf("log %q and error %q lack the node's own text", stored["node log"], row.Error)
		}
		for where, text := range stored {
			for _, secret := range []string{fixtureToken, pass} {
				if strings.Contains(text, secret) {
					t.Errorf("the %s holds secret %q unmasked:\n%s", where, secret, text)
				}
			}
		}
	})

	t.Run("ThePlanProcessMasksSecretDefaultsAndItsError", func(t *testing.T) {
		const pass = "pass-fixture-secret"
		h, res := host(t, "run-refuse", map[string]string{"refuse": "true", "pass": pass}, nil)
		if res.Status != "failed" || res.Err == nil {
			t.Fatalf("status=%s err=%v, want failed", res.Status, res.Err)
		}
		run, err := h.st.GetRun(t.Context(), "run-refuse")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(run.Error, "plan refused") {
			t.Errorf("run error = %q, want the plan's refusal", run.Error)
		}
		plan := h.plan.String()
		if !strings.Contains(plan, "plan sees def=") {
			t.Errorf("plan output lacks the plan's own text:\n%s", plan)
		}
		for where, text := range map[string]string{"run error": run.Error, "returned error": res.Err.Error(), "plan output": plan} {
			for _, secret := range []string{pass, "def-fixture-secret"} {
				if strings.Contains(text, secret) {
					t.Errorf("the %s holds secret %q unmasked:\n%s", where, secret, text)
				}
			}
		}
	})

	t.Run("APlanThatDriftsFailsTheNode", func(t *testing.T) {
		h, res := host(t, "run-drift", map[string]string{"counter": filepath.Join(t.TempDir(), "count")}, nil)
		if res.Status != "failed" {
			t.Fatalf("status=%s err=%v, want failed", res.Status, res.Err)
		}
		row := h.row(t, "run-drift", "build")
		if row.Outcome != string(sparkwing.Failed) || row.FailureReason != string(apierr.PlanDrift) {
			t.Errorf("build row outcome=%q reason=%q error=%q, want failed with plan_drift", row.Outcome, row.FailureReason, row.Error)
		}
		if strings.Contains(h.logs.text("build"), "token=") {
			t.Error("the drifted node's body ran")
		}
	})

	t.Run("AnSDKExitMidNodeFailsIt", func(t *testing.T) {
		h, res := host(t, "run-crash", map[string]string{"crash": "true"}, nil)
		if res.Status != "failed" || res.Nodes["build"].Outcome != sparkwing.Success {
			t.Fatalf("status=%s err=%v build=%+v, want failed with build still a success", res.Status, res.Err, res.Nodes["build"])
		}
		row := h.row(t, "run-crash", "crash")
		if row.Outcome != string(sparkwing.Failed) || row.FailureReason != string(apierr.SDKExited) {
			t.Errorf("crash row outcome=%q reason=%q error=%q, want failed with sdk_exited", row.Outcome, row.FailureReason, row.Error)
		}
		if _, err := os.Stat(filepath.Join(h.marker, "crash")); err != nil {
			t.Errorf("the crashed node's cleanup did not run: %v", err)
		}
	})
}

func TestAProtocolBreakAfterTheReplyFailsTheNode(t *testing.T) {
	srv, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	logs := &memLogs{records: map[string][]sparkwing.LogRecord{}}
	masker := secrets.NewMasker()
	a := &Attempts{
		Routes: srv, Resolver: fixtureResolver{}, Masker: masker,
		OpenLog: func(_ context.Context, _, node string) (NodeLog, error) { return memLog{logs: logs, node: node}, nil },
	}
	cfg := &Config{Session: SessionConfig{
		Entry: pipelineentry.Binary(os.Args[0]), Grace: 500 * time.Millisecond,
		// bug: the race runtime otherwise sleeps a second at exit, past the grace.
		Env: append(os.Environ(), fakeSDKEnv+"=sdk", "GORACE=atexit_sleep_ms=0"), Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
	}}
	run := &Run{
		ID: "run-garble", Pipeline: "p", Context: nodeproto.RunContext{RunID: "run-garble"},
		State: openStore(t), Logger: slog.New(slog.DiscardHandler), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}},
	}
	res := a.executor(cfg)(t.Context(), &NodeRun{Run: run, Plan: nodeproto.JobSpec{ID: "reply-then-garbage", Steps: []nodeproto.StepSpec{{ID: "body"}}}})
	if res.Outcome != sparkwing.Failed || res.Reason != string(apierr.ProtocolError) {
		t.Errorf("result = %+v, want failed with protocol_error despite the success reply", res)
	}
	if len(masker.Values()) == 0 {
		t.Error("the attempt's bearer was not registered with the run's masker")
	}
}
