package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
// before the execution start is recorded, and the claim token is the only
// credential the pipeline process gets.
func runLaunchedNode(ctx context.Context, controllerURL, runID, nodeID, token string, logger *slog.Logger) error {
	c := client.NewWithToken(controllerURL, nil, token)
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
	if nodeID == store.PlanNodeID {
		if err := planSDKGap(filepath.Join(src, ".sparkwing", "go.mod")); err != nil {
			return fail(err)
		}
	}
	if err := applyGoEnvFile(filepath.Join(filepath.Dir(src), GoEnvFile)); err != nil {
		return fail(err)
	}
	grant := RequestRunCacheGrant(ctx, controllerURL, token, runID, logger)
	binary, err := resolveRemoteBinary(ctx, filepath.Join(src, ".sparkwing"), controllerURL, token, runID, "", grant, logger)
	if err != nil {
		return fail(fmt.Errorf("build the pipeline: %w", err))
	}
	defer binary.release()
	args := []string{"run-node", runID, nodeID}
	if nodeID == store.PlanNodeID {
		args = []string{"plan", "--json"}
	}
	// #nosec G204,G702 -- the pipeline binary this process built, run as argv without a shell
	cmd := exec.Command(binary.path, args...)
	cmd.Dir = src
	cmd.Env = append(os.Environ(), authwire.CacheGrantEnv+"="+grant, SpecHashEnv+"="+specHash)
	cmd.Stderr = os.Stderr
	var plan bytes.Buffer
	cmd.Stdout = os.Stdout
	if nodeID == store.PlanNodeID {
		cmd.Stdout = &plan
	}
	outcome, err := runAssistedChildProcess(ctx, cmd, logger)
	switch {
	case err != nil:
		return fail(fmt.Errorf("start the pipeline: %w", err))
	case outcome.cancelCause != nil:
		return fail(fmt.Errorf("the pipeline was stopped: %w", outcome.cancelCause))
	case outcome.waitErr != nil && nodeID == store.PlanNodeID:
		return fail(fmt.Errorf("the pipeline could not plan the run (controller dispatch needs a sparkwing SDK with `plan --json`, %s or later): %w", MinPlanSDK, outcome.waitErr))
	case outcome.waitErr != nil:
		return fail(fmt.Errorf("the pipeline exited: %w", outcome.waitErr))
	case nodeID == store.PlanNodeID:
		if err := c.SubmitPlan(ctx, runID, plan.Bytes()); err != nil {
			return fail(fmt.Errorf("submit the plan: %w", err))
		}
	}
	return nil
}

// MinPlanSDK is the first sparkwing release whose pipeline binaries answer
// `plan --json`, which a controller-dispatched run plans with.
const MinPlanSDK = "v0.66.0"

const sdkModule = "github.com/sparkwing-dev/sparkwing"

// perf: a pin too old to plan fails before the build, the longest billed step.
// A pseudo-version names a commit whose history this pod cannot read, so only
// one based on MinPlanSDK or later is known to plan; a local replacement is
// judged by the plan itself, and a missing module by the build.
func planSDKGap(goMod string) error {
	// #nosec G703 -- the checkout under the SPARKWING_SOURCE_DIR the launcher Job set
	raw, err := os.ReadFile(goMod)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	mf, err := modfile.Parse(goMod, raw, nil)
	if err != nil {
		return fmt.Errorf("read the pipeline's module: %w", err)
	}
	for _, r := range mf.Replace {
		if r.Old.Path == sdkModule {
			return nil
		}
	}
	for _, r := range mf.Require {
		if r.Mod.Path != sdkModule {
			continue
		}
		v := r.Mod.Version
		if !module.IsPseudoVersion(v) {
			if semver.Compare(v, MinPlanSDK) < 0 {
				return fmt.Errorf("this repository's .sparkwing pins sparkwing %s, which lacks `plan --json`; pin %s or later to run on controller dispatch", v, MinPlanSDK)
			}
			return nil
		}
		if base, err := module.PseudoVersionBase(v); err != nil || semver.Compare(base, MinPlanSDK) < 0 {
			return fmt.Errorf("this repository's .sparkwing pins sparkwing commit %s, which cannot be checked for `plan --json` before the build; pin %s or later, or a commit after it, to run on controller dispatch", v, MinPlanSDK)
		}
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
