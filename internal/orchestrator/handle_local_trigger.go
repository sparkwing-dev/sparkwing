package orchestrator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func HandleClaimedTriggerLocal(ctx context.Context, triggerID, profileName string) error {
	logger := slog.Default()

	paths, err := DefaultPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		return fmt.Errorf("ensure sparkwing root: %w", err)
	}

	backends, releaseBackends, err := localTriggerBackends(ctx, paths, profileName)
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

	defer func() {
		if ferr := backends.State.FinishTrigger(ctx, trigger.ID); ferr != nil {
			logger.Warn("finish trigger (local) failed",
				"trigger_id", trigger.ID, "err", ferr)
		}
	}()

	var r runner.Runner
	args := resolveTriggerArgs(ctx, backends.State, trigger, logger)
	opts := Options{
		Pipeline:          trigger.Pipeline,
		RunID:             trigger.ID,
		Args:              args,
		ParentRunID:       trigger.ParentRunID,
		Admission:         pipelineAdmission(childAttachTokenFromEnv(trigger.TriggerEnv), wingwire.OriginLocal),
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
		Runner: r,
	}

	applyCheckoutProjectConfig(&opts, logger)
	res, err := Run(ctx, backends, opts)
	if err != nil {
		logger.Error(
			"run failed setup",
			"run_id", trigger.ID,
			"err", err,
		)
		return err
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
// run dispatches children that open nothing either. A named profile is
// excluded because the daemon stands in front of this machine's store alone.
func localTriggerBackends(ctx context.Context, paths Paths, profileName string) (Backends, func(), error) {
	if profileName == "" {
		opts := Options{
			DefaultStateDB: paths.StateDB(),
			Admission:      pipelineAdmission(childAttachTokenFromProcessEnv(), wingwire.OriginLocal),
		}
		if hosted, release := hostedBackendsForRun(ctx, paths, &opts); hosted.APISocket != "" {
			return hosted, release, nil
		}
	}
	st, err := openLocalTriggerStore(ctx, paths, profileName)
	if err != nil {
		return Backends{}, func() {}, err
	}
	return LocalBackends(paths, st, nil), func() { _ = st.Close() }, nil
}

func openLocalTriggerStore(ctx context.Context, paths Paths, profileName string) (*store.Store, error) {
	if profileName == "" {
		st, err := store.Open(paths.StateDB())
		if err != nil {
			return nil, fmt.Errorf("open local store: %w", err)
		}
		return st, nil
	}
	store, err := openProfileTriggerStore(ctx, paths, profileName)
	if err != nil {
		return nil, fmt.Errorf("open profile %q state: %w", profileName, err)
	}
	return store, nil
}

func openProfileTriggerStore(ctx context.Context, paths Paths, profileName string) (*store.Store, error) {
	store, err := loadProfileStateBackend(ctx, paths, profileName)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func loadProfileStateBackend(ctx context.Context, paths Paths, profileName string) (*store.Store, error) {
	p, err := profile.LoadAndResolve(profileName)
	if err != nil {
		return nil, fmt.Errorf("resolve profile %q: %w", profileName, err)
	}
	opts := Options{
		Profile:        p,
		DefaultStateDB: paths.StateDB(),
	}
	if err := ApplyProfileBackends(ctx, &opts, p); err != nil {
		return nil, err
	}
	st, ok := opts.State.(*store.Store)
	if !ok {
		return nil, fmt.Errorf("profile %q state is %T, not a local *store.Store; handle-trigger --local only supports sqlite or postgres profiles", profileName, opts.State)
	}
	return st, nil
}
