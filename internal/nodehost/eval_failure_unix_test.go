//go:build unix

package nodehost

import (
	"context"
	"log/slog"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type evalRouteListener struct{ net.Listener }

func (evalRouteListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1} }

func TestLegacyEvalFailuresNeverBecomeValues(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "eval")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, "./testdata/evalfixture").CombinedOutput(); err != nil {
		t.Fatalf("build eval fixture: %v: %s", err, out)
	}
	cfg := SessionConfig{Entry: pipelineentry.Binary(bin), Env: []string{}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "granted", Type: "local", Labels: []string{}}, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	cfg.Binding = &nodeproto.ProcessBinding{RunID: "run", JobID: "source", Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "fake-process"}}
	session, err := StartSession(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := session.Close(); err != nil {
			t.Error(err)
		}
	})
	var plan nodeproto.PlanDoc
	if err := ask(ctx, session, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: "eval", Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: "run", Pipeline: "eval"}}, &plan); err != nil {
		t.Fatal(err)
	}
	jobs := map[string]nodeproto.JobSpec{}
	for _, job := range plan.Jobs {
		jobs[job.ID] = job
	}
	t.Run("generator", func(t *testing.T) {
		nodes, err := evalGenerator(ctx, session, jobs["source"].Closures.Generate[0])
		if err == nil || !strings.Contains(err.Error(), "generator refused") || len(nodes) != 0 {
			t.Fatalf("failed generator became an expansion: %v, %v", nodes, err)
		}
	})
	t.Run("predicate", func(t *testing.T) {
		bound := cfg
		binding := *cfg.Binding
		binding.JobID = "skip"
		bound.Binding = &binding
		process, err := StartSession(ctx, bound)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := process.Close(); err != nil {
				t.Error(err)
			}
		})
		var plan nodeproto.PlanDoc
		if err := ask(ctx, process, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: "eval", Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: "run", Pipeline: "eval"}}, &plan); err != nil {
			t.Fatal(err)
		}
		skip, err := skipIfEval(ctx, process, &recorder{}, jobs["skip"].Closures.SkipIf[0], 0, time.Minute)
		if err == nil || !strings.Contains(err.Error(), "predicate refused") || skip {
			t.Fatalf("failed predicate became a value: %t, %v", skip, err)
		}
	})
	t.Run("memo", func(t *testing.T) {
		routes := &Server{ctx: ctx, listener: evalRouteListener{}, grants: map[[bearerHashSize]byte]*grant{}}
		logs := &memLogs{records: map[string][]sparkwing.LogRecord{}}
		config := Config{Session: cfg, Memo: &Memo{}, Attempts: &Attempts{Routes: routes, Resolver: fixtureResolver{}, Masker: secrets.NewMasker(), OpenLog: func(context.Context, string, string) (NodeLog, error) { return memLog{logs, "memo"}, nil }}}
		node := &NodeRun{Run: &Run{ID: "run", Pipeline: "eval", Context: nodeproto.RunContext{RunID: "run", Pipeline: "eval"}, Logger: slog.New(slog.DiscardHandler)}, Plan: jobs["memo"]}
		ran := false
		execute := memoLayer(&config, func(context.Context, *NodeRun) Result {
			ran = true
			return Result{Outcome: sparkwing.Success}
		})
		result := execute(ctx, node)
		if ran || result.Outcome != sparkwing.Failed || result.Err == nil || !strings.Contains(result.Err.Error(), "memo refused") {
			t.Fatalf("failed memo key ran body=%t, outcome=%s, error=%v", ran, result.Outcome, result.Err)
		}
	})
}
