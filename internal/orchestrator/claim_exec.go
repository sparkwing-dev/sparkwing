package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/runflags"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// GoEnvFile is where a launcher Job's init container leaves the Go module
// settings the pipeline's build needs, beside the checkout it prepared.
const GoEnvFile = "sparkwing-go.env"

// safety: a pod that dies returns its node to the queue within one lease.
const claimLease = 2 * time.Minute

var errRunCancelled = errors.New("the run was cancelled")

// safety: this is the launcher Job's trusted process; no pipeline code runs
// before the execution start is recorded, and the pipeline process gets no
// credential: it reaches the controller only through this process's routes.
func runLaunchedNode(ctx context.Context, controllerURL, runID, nodeID, token string, logger *slog.Logger) error {
	c := client.NewWithToken(controllerURL, nil, token).WithWatchObserver(NoteWatch)
	ctx, abandon := context.WithCancelCause(ctx)
	defer abandon(nil)
	if _, err := c.HeartbeatClaim(ctx, runID, nodeID, claimLease); err != nil {
		return fmt.Errorf("renew the claim: %w", err)
	}
	var wg sync.WaitGroup
	beatCtx, stopBeats := context.WithCancel(ctx)
	defer func() {
		stopBeats()
		wg.Wait()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		beatClaim(beatCtx, c, runID, nodeID, abandon, logger)
	}()
	specHash, err := c.StartClaimExecution(ctx, runID, nodeID)
	if err != nil {
		return fmt.Errorf("start the claim's execution: %w", err)
	}
	fail := func(cause error) error {
		report := store.AttemptReport{Outcome: string(sparkwing.Failed), Error: cause.Error()}
		if errors.Is(context.Cause(ctx), errRunCancelled) {
			report.Outcome = string(sparkwing.Cancelled)
		}
		// safety: the pipeline may already have reported this attempt, and a
		// second report of a claim is refused with 409, never recorded.
		if err := c.ReportAttempt(context.WithoutCancel(ctx), runID, nodeID, report); err != nil &&
			!errors.Is(err, store.ErrLockHeld) {
			return errors.Join(cause, fmt.Errorf("report the attempt: %w", err))
		}
		return cause
	}
	src := os.Getenv("SPARKWING_SOURCE_DIR")
	if err := applyGoEnvFile(filepath.Join(filepath.Dir(src), GoEnvFile)); err != nil {
		return fail(err)
	}
	binary, err := resolveLaunchedBinary(ctx, filepath.Join(src, ".sparkwing"), nil, logger)
	if err != nil {
		return fail(fmt.Errorf("build the pipeline: %w", err))
	}
	defer binary.release()
	if nodeID == store.PlanNodeID {
		return planLaunchedRun(ctx, c, src, binary.entry, runID, fail)
	}
	row, err := c.GetNode(ctx, runID, nodeID)
	if err != nil {
		return fail(fmt.Errorf("read the node's attempts: %w", err))
	}
	state := &claimState{Client: c, runID: runID, nodeID: nodeID}
	res, err := hostPodNode(ctx, podNode{
		entry: binary.entry, dir: src, controllerURL: controllerURL, token: token, runID: runID, nodeID: nodeID,
		specHash: specHash, attempt: row.AttemptsConsumed + 1, state: state, logger: logger,
	})
	if err != nil {
		return fail(err)
	}
	report := state.attempt(runner.Result{Outcome: res.Outcome, Err: res.Err}, nil)
	if errors.Is(context.Cause(ctx), errRunCancelled) {
		report.Outcome, report.FailureReason = string(sparkwing.Cancelled), ""
	}
	if err := c.ReportAttempt(context.WithoutCancel(ctx), runID, nodeID, report); err != nil {
		return fmt.Errorf("report the attempt: %w", err)
	}
	if !sparkwing.Outcome(report.Outcome).OK() {
		return fmt.Errorf("node %s/%s %s: %s", runID, nodeID, report.Outcome, report.Error)
	}
	return nil
}

// hack: a variable so tests stand in a prebuilt binary for the build.
var resolveLaunchedBinary = resolveRemoteBinary

// safety: the plan is the team's code, so it runs in the pipeline's own
// process with the minimal environment, and only the host's stamped and
// validated plan document reaches the controller.
func planLaunchedRun(ctx context.Context, c *client.Client, src string, entry pipelineentry.Command, runID string, fail func(error) error) error {
	run, err := c.GetRunForExecution(ctx, runID)
	if err != nil {
		return fail(fmt.Errorf("read run %s: %w", runID, err))
	}
	requires, err := PipelineRequires(src, run.Pipeline)
	if err != nil {
		return fail(err)
	}
	flags := runflags.FromInvocation(run.Invocation)
	args, guards, err := checkoutRunArgs(src, run.Pipeline, run.Args)
	if err != nil {
		return fail(err)
	}
	recorded, _ := run.Invocation[runflags.DefaultBranchKey].(string)
	checkArgs := guardCheck(run.Pipeline, guards, pipelines.GuardContext{GitBranch: run.GitBranch}, func() string {
		return checkoutDefaultBranch(ctx, recorded, src)
	})
	masker := secrets.NewMasker()
	env, err := podProcessEnv(masker, slog.Default())
	if err != nil {
		return fail(err)
	}
	sink, err := newPodPlanSink(ctx, runID, masker, os.Stderr)
	if err != nil {
		return fail(err)
	}
	plan, err := nodehost.PlanDocument(ctx, nodehost.SessionConfig{Entry: entry, Dir: src, Sink: sink, Env: env},
		nodehost.PlanRequest{
			Pipeline: run.Pipeline, Args: args, Run: podRunContext(run), Requires: requires, Masker: masker,
			StartAt: flags.StartAt, StopAt: flags.StopAt, Only: flags.Only, CheckArgs: checkArgs,
		})
	sink.end()
	if err != nil {
		return fail(fmt.Errorf("the pipeline could not plan the run: %s", masker.Mask(err.Error())))
	}
	if err := c.SubmitPlan(ctx, runID, plan.JSON); err != nil {
		return fail(fmt.Errorf("submit the plan: %w", err))
	}
	return nil
}

// safety: a refused beat means the claim was lost, superseded or can no
// longer be paid for, and a controller silent for a whole lease has let it
// lapse; either way the pipeline stops rather than bill past its claim.
func beatClaim(ctx context.Context, c *client.Client, runID, nodeID string, abandon context.CancelCauseFunc, logger *slog.Logger) {
	lastOK := time.Now()
	t := time.NewTicker(store.DispatchedHeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cancel, err := c.HeartbeatClaim(ctx, runID, nodeID, claimLease)
		switch {
		case err == nil && cancel:
			abandon(errRunCancelled)
			return
		case err == nil:
			lastOK = time.Now()
		case ctx.Err() != nil:
			return
		case errors.Is(err, store.ErrLockHeld), time.Since(lastOK) >= claimLease:
			logger.Error("launched node: the claim is no longer held; stopping", "run_id", runID, "node_id", nodeID, "err", err)
			abandon(fmt.Errorf("%w: %w", errClaimAbandoned, err))
			return
		default:
			logger.Warn("launched node: claim heartbeat failed", "run_id", runID, "node_id", nodeID, "err", err)
		}
	}
}

// safety: the init container wrote this before any pipeline code ran, and
// only the module settings it may name are read back from it.
func applyGoEnvFile(path string) error {
	// #nosec G703 -- beside the SPARKWING_SOURCE_DIR the launcher Job set
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, value, _ := strings.Cut(sc.Text(), "=")
		if name == "GOPRIVATE" || name == "GONOSUMDB" {
			if err := os.Setenv(name, value); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
