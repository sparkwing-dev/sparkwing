package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	localrunner "github.com/sparkwing-dev/sparkwing/internal/runners/local"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// hack: only tests reach this spike. The engine owns the run row, the DAG and
// dispatch, and drives the pipeline binary only to evaluate the plan and run one
// node per process; design/engine-hosted-execution.md is the design.
type hostedRun struct {
	Describe []byte
	Binary   string
	Pipeline string
	WorkDir  string
	Paths    Paths
	Logger   *slog.Logger
	Delegate sparkwing.Logger
}

type hostedRunResult struct {
	RunID string
	Order []string
	Nodes map[string]runner.Result
}

func runHosted(ctx context.Context, cfg hostedRun) (res hostedRunResult, err error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if err := hostedDescribes(cfg.Describe, cfg.Pipeline); err != nil {
		return hostedRunResult{}, err
	}
	if err := cfg.Paths.EnsureRoot(); err != nil {
		return hostedRunResult{}, err
	}
	st, err := store.Open(cfg.Paths.StateDB()) //nolint:contextcheck // store.Open takes no context.
	if err != nil {
		return hostedRunResult{}, fmt.Errorf("hosted run: open state: %w", err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			cfg.Logger.Warn("hosted run: close state", "err", cerr)
		}
	}()

	runID := newRunID()
	now := time.Now().UTC()
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: cfg.Pipeline, Status: "running", StartedAt: now, CreatedAt: now}); err != nil {
		return hostedRunResult{}, fmt.Errorf("hosted run: create run: %w", err)
	}
	res = hostedRunResult{RunID: runID, Nodes: map[string]runner.Result{}}
	var snap planSnapshot
	// safety: registered before every later defer, so it runs after the heartbeat
	// and controller stop, and no return after CreateRun leaves the row running.
	defer func() {
		hostedFinalize(ctx, st, cfg.Logger, runID, snap, res.Nodes)
		status, msg := "success", ""
		for id, r := range res.Nodes {
			if r.Outcome != sparkwing.Success {
				status, msg = "failed", fmt.Sprintf("node %s: %s", id, r.Outcome)
			}
		}
		if err != nil {
			status, msg = "failed", err.Error()
		}
		if cause := context.Cause(ctx); cause != nil {
			status, msg = "cancelled", cause.Error()
		}
		if ferr := st.FinishRun(context.WithoutCancel(ctx), runID, status, msg); ferr != nil && err == nil {
			err = ferr
		}
	}()
	wedge, err := storeWedgeBudget()
	if err != nil {
		return res, err
	}
	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		runRunHeartbeatLoop(hbCtx, 30*time.Second, localState{st: st}, runID, wedge)
	}()
	defer func() { stopHeartbeat(); <-hbDone }()
	art, err := localArtifactStore(cfg.Paths)
	if err != nil {
		return res, err
	}
	loopback, err := startLoopbackController(ctx, st, art, runID, cfg.Logger)
	if err != nil {
		return res, err
	}
	defer loopback.Close() //nolint:contextcheck // Close revokes the run token after ctx may have ended.

	var raw []byte
	snap, raw, err = hostedPlan(ctx, cfg, runID, loopback.url, loopback.token)
	if err == nil {
		err = hostedAdmit(ctx, st, runID, snap)
	}
	if err == nil {
		if perr := st.UpdatePlanSnapshot(ctx, runID, raw); perr != nil {
			err = fmt.Errorf("hosted run: persist plan snapshot: %w", perr)
		}
	}
	if err == nil {
		lr := localrunner.New(client.NewWithToken(loopback.url, nil, loopback.token), localrunner.Config{
			Executable:    cfg.Binary,
			ControllerURL: loopback.url,
			AgentToken:    loopback.token,
			WorkDir:       cfg.WorkDir,
			Home:          cfg.Paths.Root,
			Logger:        cfg.Logger,
		})
		res.Order, res.Nodes = hostedSchedule(ctx, st, lr, cfg, runID, snap)
	}
	return res, err
}

func hostedDescribes(doc []byte, pipeline string) error {
	var described []sparkwing.DescribePipeline
	if err := json.Unmarshal(doc, &described); err != nil {
		return fmt.Errorf("hosted run: describe document: %w", err)
	}
	for _, p := range described {
		if p.Name == pipeline {
			return nil
		}
	}
	return fmt.Errorf("hosted run: the describe document has no pipeline %q", pipeline)
}

// safety: describe carries no nodes, so the binary evaluates the plan for the
// run row the engine already wrote.
func hostedPlan(ctx context.Context, cfg hostedRun, runID, controllerURL, token string) (planSnapshot, []byte, error) {
	var snap planSnapshot
	cmd := exec.CommandContext(ctx, cfg.Binary, "plan", "--json")
	cmd.Dir = cfg.WorkDir
	cmd.Env = append(os.Environ(),
		"SPARKWING_HOME="+cfg.Paths.Root,
		"SPARKWING_RUN_ID="+runID,
		"SPARKWING_CONTROLLER_URL="+controllerURL,
		"SPARKWING_AGENT_TOKEN="+token,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return snap, nil, fmt.Errorf("hosted run: plan --json: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	if err := json.Unmarshal(out, &snap); err != nil {
		return snap, nil, fmt.Errorf("hosted run: plan document: %w", err)
	}
	return snap, out, nil
}

// safety: a node needing behavior the spike does not host is refused before
// any row is written, so a plan never half-runs.
func hostedAdmit(ctx context.Context, st *store.Store, runID string, snap planSnapshot) error {
	if err := hostedCheck(snap); err != nil {
		return err
	}
	for _, n := range snap.Nodes {
		if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: n.ID, Status: "pending", Deps: n.Deps, RequestedSlots: 1}); err != nil {
			return fmt.Errorf("hosted run: create node %s: %w", n.ID, err)
		}
	}
	return nil
}

func hostedCheck(snap planSnapshot) error {
	if len(snap.Requires) > 0 {
		return fmt.Errorf("hosted run: the pipeline requires runner labels %v, which the hosted spike does not place", snap.Requires)
	}
	if snap.PlanConc != nil || len(snap.PlanConcs) > 0 {
		return errors.New("hosted run: the pipeline joins a plan-level concurrency group, which the hosted spike does not acquire")
	}
	if snap.Priority != 0 || snap.AdmissionClass != "" || snap.ClaimWaitMS != 0 || snap.Resources != nil {
		return errors.New("hosted run: the pipeline sets admission priority, class, claim wait or resources, which the hosted spike does not admit")
	}
	deps := make(map[string][]string, len(snap.Nodes))
	for _, n := range snap.Nodes {
		if _, dup := deps[n.ID]; dup {
			return fmt.Errorf("hosted run: the plan holds node %s twice", n.ID)
		}
		deps[n.ID] = n.Deps
	}
	for _, n := range snap.Nodes {
		if why := hostedUnsupported(n); why != "" {
			return fmt.Errorf("hosted run: node %s %s, which the hosted spike does not run", n.ID, why)
		}
		for _, d := range n.Deps {
			if _, ok := deps[d]; !ok {
				return fmt.Errorf("hosted run: node %s needs %s, which the plan does not hold", n.ID, d)
			}
		}
	}
	const visiting, visited = 1, 2
	state := make(map[string]int, len(deps))
	var visit func(id string, path []string) error
	visit = func(id string, path []string) error {
		switch state[id] {
		case visited:
			return nil
		case visiting:
			return fmt.Errorf("hosted run: the plan has a dependency cycle %s", strings.Join(append(path, id), " -> "))
		}
		state[id] = visiting
		for _, d := range deps[id] {
			if err := visit(d, append(path, id)); err != nil {
				return err
			}
		}
		state[id] = visited
		return nil
	}
	for _, n := range snap.Nodes {
		if err := visit(n.ID, nil); err != nil {
			return err
		}
	}
	return nil
}

func hostedUnsupported(n snapshotNode) string {
	switch {
	case n.Dynamic:
		return "is generated at run time"
	case n.Approval != nil:
		return "is an approval gate"
	case n.OnFailureOf != "":
		return "is an OnFailure recovery"
	case len(n.OptionalDeps) > 0:
		return "has optional dependencies"
	}
	m := n.Modifiers
	if m == nil {
		return ""
	}
	switch {
	case m.Retry > 0 || m.RetryAuto:
		return "retries"
	case m.TimeoutMS > 0 || m.NoProgressTimeoutMS > 0:
		return "has a timeout"
	case m.Cache:
		return "is memoized"
	case m.ConcGroup != "":
		return "joins a concurrency group"
	case m.HasBeforeRun || m.HasAfterRun || m.HasSkipIf:
		return "has a BeforeRun, AfterRun or SkipIf closure"
	case m.Optional || m.ContinueOnError || m.OnFailure != "":
		return "changes how its failure propagates"
	case m.Inline:
		return "runs inline"
	case len(m.RunsOn) > 0 || len(m.Prefers) > 0 || len(m.WhenRunner) > 0:
		return "has a runner placement condition"
	case m.ResCores > 0 || m.ResMemoryBytes > 0:
		return "reserves resources"
	}
	return ""
}

type hostedDone struct {
	id  string
	res runner.Result
}

func hostedSchedule(ctx context.Context, st *store.Store, lr runner.Runner, cfg hostedRun, runID string, snap planSnapshot) ([]string, map[string]runner.Result) {
	results := make(map[string]runner.Result, len(snap.Nodes))
	started := map[string]bool{}
	var order []string
	done := make(chan hostedDone)
	running := 0
	for {
		progressed := false
		for _, n := range snap.Nodes {
			if started[n.ID] {
				continue
			}
			if ctx.Err() != nil {
				started[n.ID], progressed = true, true
				results[n.ID] = runner.Result{Outcome: sparkwing.Cancelled, Err: ctx.Err()}
				continue
			}
			ready, blocked := true, ""
			for _, d := range n.Deps {
				r, finished := results[d]
				if !finished {
					ready = false
					break
				}
				if r.Outcome != sparkwing.Success {
					blocked = d
				}
			}
			if !ready {
				continue
			}
			started[n.ID], progressed = true, true
			if blocked != "" {
				msg := "dependency " + blocked + " did not succeed"
				if err := st.FinishNode(context.WithoutCancel(ctx), runID, n.ID, string(sparkwing.Skipped), msg, nil); err != nil {
					cfg.Logger.Warn("hosted run: record skipped node", "node", n.ID, "err", err)
				}
				results[n.ID] = runner.Result{Outcome: sparkwing.Skipped}
				continue
			}
			order = append(order, n.ID)
			running++
			go func(id string) {
				done <- hostedDone{id: id, res: hostedRunNode(ctx, st, lr, cfg, runID, id)}
			}(n.ID)
		}
		if running == 0 && !progressed {
			// safety: admission refuses cycles, so a node left unstarted here means
			// the scheduler is wrong; it fails rather than reading as a success.
			for _, n := range snap.Nodes {
				if started[n.ID] {
					continue
				}
				err := fmt.Errorf("hosted run: node %s never became ready", n.ID)
				if ferr := st.FinishNode(context.WithoutCancel(ctx), runID, n.ID, string(sparkwing.Failed), err.Error(), nil); ferr != nil {
					cfg.Logger.Warn("hosted run: record stalled node", "node", n.ID, "err", ferr)
				}
				results[n.ID] = runner.Result{Outcome: sparkwing.Failed, Err: err}
			}
			return order, results
		}
		if running > 0 && !progressed {
			d := <-done
			running--
			results[d.id] = d.res
		}
	}
}

// safety: the local runner leaves a cancelled node's row to dispatcher
// teardown, and the spike is that dispatcher, so no row outlives the run unfinished.
func hostedFinalize(ctx context.Context, st *store.Store, logger *slog.Logger, runID string, snap planSnapshot, results map[string]runner.Result) {
	outcome, reason := sparkwing.Failed, "the node ended without a terminal record"
	if ctx.Err() != nil {
		outcome, reason = sparkwing.Cancelled, "ctx-cancelled"
	}
	finishCtx := context.WithoutCancel(ctx)
	for _, n := range snap.Nodes {
		row, err := st.GetNode(finishCtx, runID, n.ID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && runner.NodeTerminal(row)) {
			continue
		}
		if err == nil {
			err = st.FinishNode(finishCtx, runID, n.ID, string(outcome), reason, nil)
		}
		if err != nil {
			logger.Warn("hosted run: finalize node", "node", n.ID, "err", err)
		}
		results[n.ID] = runner.Result{Outcome: outcome, Err: errors.New(reason)}
	}
}

func hostedRunNode(ctx context.Context, st *store.Store, lr runner.Runner, cfg hostedRun, runID, nodeID string) runner.Result {
	if err := st.StartNode(ctx, runID, nodeID); err != nil {
		return runner.Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("hosted run: start node %s: %w", nodeID, err)}
	}
	res := lr.RunNode(ctx, runner.Request{RunID: runID, NodeID: nodeID, Pipeline: cfg.Pipeline, Delegate: cfg.Delegate})
	recordNodeUsage(ctx, Backends{State: localState{st: st}, LocalCoordination: true}, runID, nodeID, res.Usage)
	if res.Outcome == "" {
		res.Outcome = sparkwing.Failed
		res.Err = errors.Join(res.Err, fmt.Errorf("hosted run: node %s reported no outcome", nodeID))
	}
	return res
}
