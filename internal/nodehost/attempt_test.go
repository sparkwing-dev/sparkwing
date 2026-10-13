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

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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

type attemptHarness struct {
	st     *store.Store
	logs   *memLogs
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
	host := func(t *testing.T, runID string, args map[string]string) (*attemptHarness, RunResult) {
		t.Helper()
		srv, err := Start(nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = srv.Close() })
		h := &attemptHarness{st: openStore(t), logs: &memLogs{records: map[string][]sparkwing.LogRecord{}}, marker: t.TempDir(), usage: map[string]Usage{}}
		args["marker"] = h.marker
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		res, err := Execute(ctx, Config{
			Session: SessionConfig{
				Entry: bin, Dir: t.TempDir(), Grace: 2 * time.Second,
				Env: append(os.Environ(), "SPARKWING_LEAK=engine"),
			},
			Pipeline: "attempts", Args: args,
			Run:   nodeproto.RunContext{RunID: runID},
			State: h.st, Logger: slog.New(slog.DiscardHandler), Heartbeat: time.Hour,
			Attempts: &Attempts{
				Routes: srv, Resolver: fixtureResolver{}, Masker: secrets.NewMasker(),
				OpenLog: func(_ context.Context, _, node string) (NodeLog, error) {
					h.logs.mu.Lock()
					defer h.logs.mu.Unlock()
					h.logs.open++
					return memLog{logs: h.logs, node: node}, nil
				},
				Usage: func(n *NodeRun, u Usage) {
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
		h, res := host(t, "run-attempt", map[string]string{})
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
		var out struct{ Env, Run, Leak string }
		if err := json.Unmarshal(raw, &out); err != nil || out.Env != "from-plan" || out.Run != "run-attempt" || out.Leak != "" {
			t.Errorf("build output = %s (err %v), want the node env and run id and no engine SPARKWING_ variable", raw, err)
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
		if _, err := os.Stat(filepath.Join(h.marker, "build")); err != nil {
			t.Errorf("the node's cleanup did not run: %v", err)
		}
		if u, ok := h.usage["build"]; !ok || u.Wall <= 0 {
			t.Errorf("usage hook got %+v (called %v)", u, ok)
		}
		run, err := h.st.GetRun(t.Context(), "run-attempt")
		if err != nil {
			t.Fatal(err)
		}
		var snap nodeproto.PlanDoc
		if err := json.Unmarshal(run.PlanSnapshot, &snap); err != nil || len(snap.Nodes) != 1 || !strings.HasPrefix(snap.Nodes[0].SpecHash, "sha256:") {
			t.Errorf("plan snapshot = %s (err %v), want each node stamped with its spec_hash", run.PlanSnapshot, err)
		}
	})

	t.Run("APlanThatDriftsFailsTheNode", func(t *testing.T) {
		h, res := host(t, "run-drift", map[string]string{"counter": filepath.Join(t.TempDir(), "count")})
		if res.Status != "failed" {
			t.Fatalf("status=%s err=%v, want failed", res.Status, res.Err)
		}
		row := h.row(t, "run-drift", "build")
		if row.Outcome != string(sparkwing.Failed) || row.FailureReason != string(nodeproto.CodePlanDrift) {
			t.Errorf("build row outcome=%q reason=%q error=%q, want failed with plan_drift", row.Outcome, row.FailureReason, row.Error)
		}
		if strings.Contains(h.logs.text("build"), "token=") {
			t.Error("the drifted node's body ran")
		}
	})

	t.Run("AnSDKExitMidNodeFailsIt", func(t *testing.T) {
		h, res := host(t, "run-crash", map[string]string{"crash": "true"})
		if res.Status != "failed" || res.Nodes["build"].Outcome != sparkwing.Success {
			t.Fatalf("status=%s err=%v build=%+v, want failed with build still a success", res.Status, res.Err, res.Nodes["build"])
		}
		row := h.row(t, "run-crash", "crash")
		if row.Outcome != string(sparkwing.Failed) || row.FailureReason != string(nodeproto.CodeSDKExited) {
			t.Errorf("crash row outcome=%q reason=%q error=%q, want failed with sdk_exited", row.Outcome, row.FailureReason, row.Error)
		}
		if _, err := os.Stat(filepath.Join(h.marker, "crash")); err != nil {
			t.Errorf("the crashed node's cleanup did not run: %v", err)
		}
	})
}

func TestSpecHashIgnoresKeyOrderAndItsOwnField(t *testing.T) {
	a, err := nodeproto.SpecHash([]byte(`{"id":"a","deps":[],"env":{"X":"1","Y":"<2>"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := nodeproto.SpecHash([]byte(`{ "env": {"Y":"<2>","X":"1"}, "spec_hash":"sha256:old", "deps":[], "id":"a" }`))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := nodeproto.SpecHash([]byte(`{"id":"a","deps":[],"env":{"X":"1","Y":"<3>"}}`))
	if a != b || a == c {
		t.Errorf("hashes a=%s b=%s c=%s, want a == b != c", a, b, c)
	}
}
