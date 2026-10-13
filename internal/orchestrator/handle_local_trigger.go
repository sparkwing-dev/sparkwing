package orchestrator

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func HandleClaimedTriggerLocal(ctx context.Context, triggerID string) error {
	logger := slog.Default()

	paths, err := DefaultPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		return fmt.Errorf("ensure sparkwing root: %w", err)
	}

	backends, selection, releaseBackends, err := localTriggerBackends(ctx, paths)
	if err != nil {
		return err
	}
	defer releaseBackends()

	trigger, err := backends.State.GetTrigger(ctx, triggerID)
	if err != nil {
		return fmt.Errorf("get trigger %s: %w", triggerID, err)
	}
	logger.Info(
		"handling claimed trigger (local)",
		"run_id", trigger.ID,
		"pipeline", trigger.Pipeline,
		"parent_run_id", trigger.ParentRunID,
	)

	finishTrigger := true
	defer func() {
		if !finishTrigger {
			return
		}
		if ferr := backends.State.FinishTrigger(ctx, trigger.ID); ferr != nil {
			logger.Warn("finish trigger (local) failed",
				"trigger_id", trigger.ID, "err", ferr)
		}
	}()

	attachToken := cmp.Or(childAttachTokenFromEnv(trigger.TriggerEnv), childAttachTokenFromProcessEnv())
	warnUnattachedChild(logger, trigger, attachToken, backends.LocalCoordination)
	args := resolveTriggerArgs(ctx, backends.State, trigger, logger)
	opts := Options{
		standalone:        childStandalone(backends, selection),
		Pipeline:          trigger.Pipeline,
		RunID:             trigger.ID,
		Args:              args,
		Priority:          submittedTriggerPriority(trigger),
		ParentRunID:       trigger.ParentRunID,
		Admission:         pipelineAdmission(attachToken, wingwire.OriginLocal),
		DryRun:            trigger.TriggerEnv[envDryRun] == "1",
		RetryOf:           trigger.RetryOf,
		RetrySource:       trigger.RetrySource,
		RetryRepoDir:      trigger.TriggerEnv[retryprovenance.RepoDirKey],
		RetryRepoIdentity: trigger.TriggerEnv[retryprovenance.RepoIdentityKey],
		RetryRevision:     trigger.TriggerEnv[retryprovenance.RevisionKey],
		RetryPlanHash:     trigger.TriggerEnv[retryprovenance.PlanHashKey],
		Full:              trigger.Full,
		Trigger: sparkwing.TriggerInfo{
			Source:      trigger.TriggerSource,
			User:        trigger.TriggerUser,
			PullRequest: sparkwing.PullRequestFromEnv(trigger.TriggerEnv),
		},
		Git: sparkwing.NewGit(
			sparkwing.CurrentRuntime().WorkDir,
			trigger.GitSHA, trigger.GitBranch, "", trigger.Repo, trigger.RepoURL,
		),
	}

	pinErr := applyCheckoutProjectConfig(&opts)
	if pinErr == nil {
		pinErr = refusePinnedCheckoutRun(opts.PipelineYAML)
	}
	if pinErr != nil {
		if ferr := recordClaimedTriggerSetupFailure(ctx, backends.State, trigger, pinErr); ferr != nil {
			finishTrigger = false
			logger.Error("record failed trigger run (local)", "run_id", trigger.ID, "err", ferr)
		}
		logger.Error("run refused", "run_id", trigger.ID, "err", pinErr)
		return nil
	}
	execution, err := setupLocalExecution(ctx, paths, &opts, backends, nodeWorkspace(), logger)
	if err != nil {
		return err
	}
	defer execution.cleanup()
	execution.runner.SetLeaseTokenSource(leaseTokensFromContext)
	opts.Runner = NewNodeExecutor(backends).WithSpawner(execution.runner)
	res, err := Run(ctx, backends, opts)
	if err != nil {
		if ferr := recordClaimedTriggerSetupFailure(ctx, backends.State, trigger, err); ferr != nil {
			finishTrigger = false
			logger.Error("record failed trigger run (local)", "run_id", trigger.ID, "err", ferr)
		}
		logger.Error(
			"run failed setup",
			"run_id", trigger.ID,
			"err", err,
		)
		return nil
	}
	logger.Info(
		"run finished (local)",
		"run_id", res.RunID,
		"pipeline", trigger.Pipeline,
		"status", res.Status,
	)
	return nil
}

// safety: a child run picks its backend the way its parent did, so a hosted
// run dispatches children that open nothing either and an unhosted one hands
// its child the very store its trigger row lives in.
func localTriggerBackends(ctx context.Context, paths Paths) (Backends, hostedSelection, func(), error) {
	opts := Options{
		DefaultStateDB: paths.StateDB(),
		Admission:      pipelineAdmission(childAttachTokenFromProcessEnv(), wingwire.OriginLocal),
	}
	hosted, selection, release, err := hostedBackendsForRun(ctx, paths, &opts)
	if err != nil {
		return Backends{}, hostedSelection{}, func() {}, err
	}
	if hosted.APISocket != "" {
		return hosted, selection, release, nil
	}
	st, err := openLocalTriggerStore(paths)
	if err != nil {
		return Backends{}, selection, func() {}, err
	}
	closeStore := func() {
		if err := st.Close(); err != nil {
			slog.Default().Warn("close local store", "err", err)
		}
	}
	return LocalBackends(paths, st, nil), selection, closeStore, nil
}

// safety: the parent names the file, and a child never derives a standalone
// path of its own: the trigger row it is about to read lives in whatever store
// the process that claimed it was using.
func parentStateDB() string { return strings.TrimSpace(os.Getenv(StandaloneStateDBEnv)) }

func parentStandaloneReason() string { return strings.TrimSpace(os.Getenv(StandaloneReasonEnv)) }

// safety: only a child that did not reach the daemon inherits the parent's
// answer. A hosted child that read these would record a store it never opened
// and re-export that path to its own children.
func childStandalone(backends Backends, sel hostedSelection) *standaloneRun {
	if backends.APISocket != "" {
		return nil
	}
	path, reason := parentStateDB(), parentStandaloneReason()
	if path == "" && reason == "" {
		return nil
	}
	return &standaloneRun{reason: reason, stateDB: path, announced: true}
}

func openLocalTriggerStore(paths Paths) (*store.Store, error) {
	path := parentStateDB()
	if path == "" {
		path = paths.StateDB()
	}
	st, err := storeOpen(path)
	if err != nil {
		return nil, fmt.Errorf("open local store %s: %w", path, err)
	}
	return st, nil
}

// bug: an awaited child claimed outside its parent's trigger loop, such as by the resident consumer, has no
// lease to attach to and queues on its own, possibly behind the parent waiting on it; it is said, not hidden.
func warnUnattachedChild(logger *slog.Logger, trigger *store.Trigger, attachToken string, coordinated bool) {
	awaited := trigger.ParentRunID != "" && strings.HasPrefix(trigger.TriggerSource, "await-pipeline")
	if attachToken != "" || !awaited || !coordinated {
		return
	}
	logger.Warn("awaited child run has no parent lease to attach to; it is admitted on its own and may "+
		"queue behind the parent run waiting on it", "run_id", trigger.ID, "parent_run_id", trigger.ParentRunID)
}
