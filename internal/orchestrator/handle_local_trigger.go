// Local-mode child entry point for laptop cross-repo dispatch.
// LocalBackends against the parent's SQLite (or the resolved profile's
// state backend, when --profile is passed); no HTTP, no heartbeat.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// HandleClaimedTriggerLocal runs an already-claimed trigger to
// terminal against LocalBackends. Trigger MUST already be 'claimed';
// re-claiming would race the parent's lease bookkeeping.
//
// profileName, when non-empty, opens the named profile's state backend
// instead of the default local sqlite. The parent's local trigger
// dispatcher forwards its own active profile so the child sees the
// same triggers table (matters whenever state is non-sqlite, chiefly
// the postgres-local path).
func HandleClaimedTriggerLocal(ctx context.Context, triggerID, profileName string) error {
	logger := slog.Default()

	paths, err := DefaultPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		return fmt.Errorf("ensure sparkwing root: %w", err)
	}

	st, err := openLocalTriggerStore(ctx, paths, profileName)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	trigger, err := st.GetTrigger(ctx, triggerID)
	if err != nil {
		return fmt.Errorf("get trigger %s: %w", triggerID, err)
	}
	logger.Info(
		"handling claimed trigger (local)",
		"run_id", trigger.ID,
		"pipeline", trigger.Pipeline,
		"parent_run_id", trigger.ParentRunID,
	)

	backends := LocalBackends(paths, st, nil)

	defer func() {
		if ferr := st.FinishTrigger(ctx, trigger.ID); ferr != nil {
			logger.Warn("finish trigger (local) failed",
				"trigger_id", trigger.ID, "err", ferr)
		}
	}()

	cancelled := &atomic.Bool{}
	_ = cancelled

	var r runner.Runner
	args := resolveTriggerArgs(ctx, backends.State, trigger, logger)
	inheritedAdmission := planAdmissionFromTriggerEnv(trigger.TriggerEnv)
	res, err := Run(ctx, backends, Options{
		Pipeline:                   trigger.Pipeline,
		RunID:                      trigger.ID,
		Args:                       args,
		ParentRunID:                trigger.ParentRunID,
		InheritedPlanCacheKey:      inheritedAdmission.Key,
		InheritedPlanCacheHolderID: inheritedAdmission.HolderID,
		RetryOf:                    trigger.RetryOf,
		RetrySource:                trigger.RetrySource,
		Full:                       trigger.Full,
		Trigger: sparkwing.TriggerInfo{
			Source: trigger.TriggerSource,
			User:   trigger.TriggerUser,
		},
		Git: sparkwing.NewGit(
			sparkwing.CurrentRuntime().WorkDir,
			trigger.GitSHA, trigger.GitBranch, "", trigger.Repo, trigger.RepoURL,
		),
		Runner: r,
	})
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

// openLocalTriggerStore returns a *store.Store for handle-trigger to
// poll. With profileName empty it opens the default local sqlite at
// paths.StateDB() (the historical behavior). With a profileName it
// resolves that profile and opens its declared state surface, so the
// child sees the same triggers row the parent enqueued.
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

// openProfileTriggerStore loads profiles.yaml, resolves profileName,
// applies the profile's surfaces to a transient Options, and returns
// the resulting state backend. Errors clearly when the profile's state
// isn't a *store.Store (e.g. controller-backed) -- handle-trigger
// --local can only adopt triggers from a local-store-backed profile.
func openProfileTriggerStore(ctx context.Context, paths Paths, profileName string) (*store.Store, error) {
	store, err := loadProfileStateBackend(ctx, paths, profileName)
	if err != nil {
		return nil, err
	}
	return store, nil
}

// loadProfileStateBackend resolves a profile name to a *store.Store via
// the same plumbing the parent uses (ApplyProfileBackends). Returns a
// clear error when the profile resolves to a non-local state backend
// (controller, S3) since handle-trigger --local cannot adopt those.
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
