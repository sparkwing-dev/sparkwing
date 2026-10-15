package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/internal/runflags"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// HostClaimedTrigger runs the local trigger triggerID, which another process
// claimed, on the engine host: each node runs in its own process of program,
// the compiled pipeline. The claiming process starts this one in the
// trigger's checkout with the run's environment, and it finishes the trigger
// when the run ends.
func HostClaimedTrigger(ctx context.Context, triggerID, program string) error {
	logger := slog.Default()
	paths, err := DefaultPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		return fmt.Errorf("ensure sparkwing root: %w", err)
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	// safety: the claiming process started the admission daemon; this host never starts one, which would be
	// its own descendant and reaped with it, so a daemon gone by now leaves the run to the standalone path.
	admission := pipelineAdmission("", wingwire.OriginLocal)
	admission.Spawn = wingdclient.NoHostSpawn
	backends, selection, releaseBackends, err := localTriggerBackends(ctx, paths, admission)
	if err != nil {
		return err
	}
	defer releaseBackends()

	trigger, err := backends.State.GetTrigger(ctx, triggerID)
	if err != nil {
		return fmt.Errorf("get trigger %s: %w", triggerID, err)
	}
	finishTrigger := true
	defer func() {
		if !finishTrigger {
			return
		}
		if ferr := backends.State.FinishTrigger(context.WithoutCancel(ctx), trigger.ID); ferr != nil {
			logger.Warn("finish trigger (local) failed", "trigger_id", trigger.ID, "err", ferr)
		}
	}()
	refuse := func(cause, recorded error) error {
		logger.Error("run failed setup", "run_id", trigger.ID, "err", cause)
		if ferr := recordClaimedTriggerSetupFailure(context.WithoutCancel(ctx), backends.State, trigger, recorded); ferr != nil {
			finishTrigger = false
			logger.Error("record failed trigger run (local)", "run_id", trigger.ID, "err", ferr)
		}
		return nil
	}

	admission.ParentRunID, admission.ParentNodeID = trigger.ParentRunID, trigger.ParentNodeID
	flags := runflags.FromTriggerEnv(trigger.TriggerEnv)
	opts := Options{
		standalone:        childStandalone(backends, selection),
		Pipeline:          trigger.Pipeline,
		RunID:             trigger.ID,
		Args:              resolveTriggerArgs(ctx, backends.State, trigger, logger),
		Priority:          submittedTriggerPriority(trigger),
		ParentRunID:       trigger.ParentRunID,
		Admission:         admission,
		DryRun:            flags.DryRun,
		NoCache:           flags.NoCache,
		StartAt:           flags.StartAt,
		StopAt:            flags.StopAt,
		Only:              flags.Only,
		RetryOf:           trigger.RetryOf,
		RetrySource:       trigger.RetrySource,
		RetryRepoDir:      trigger.TriggerEnv[retryprovenance.RepoDirKey],
		RetryRepoIdentity: trigger.TriggerEnv[retryprovenance.RepoIdentityKey],
		RetryRevision:     trigger.TriggerEnv[retryprovenance.RevisionKey],
		RetryPlanHash:     trigger.TriggerEnv[retryprovenance.PlanHashKey],
		Full:              trigger.Full,
		MaxParallel:       runtime.NumCPU(),
		Delegate:          quietRunLog{},
		Trigger: sparkwing.TriggerInfo{
			Source:      trigger.TriggerSource,
			User:        trigger.TriggerUser,
			PullRequest: controller.PullRequestForTrigger(trigger.TriggerEnv),
		},
		Git: &sparkwing.Git{SHA: trigger.GitSHA, Branch: trigger.GitBranch, Repo: trigger.Repo, RepoURL: trigger.RepoURL},
	}
	// safety: the run keeps the store this process read its trigger from, which is a standalone parent's own.
	if local, ok := backends.State.(localState); ok {
		opts.State = local.st
	}
	command := pipelineentry.Binary(program)
	doc, err := repos.Describe(ctx, command, dir)
	if err != nil {
		err = fmt.Errorf("describe %s: %w", program, err)
		return refuse(err, err)
	}
	entry := HostedEntry{
		Command: command, Dir: dir, Env: os.Environ(),
		Engine:           nodeproto.EngineIdentity{Version: sparkwingModuleVersion()},
		Runner:           nodeproto.RunnerIdentity{Name: boxHostID(), Type: "local", Labels: []string{}},
		PipelineRevision: trigger.TriggerEnv[PipelineRevKey], BinarySource: "source",
	}
	if trigger.TriggerEnv[crons.PinnedBinaryEnvKey] != "" {
		entry.BinarySource = "pinned"
	}
	for _, p := range doc.Pipelines {
		if p.Name != trigger.Pipeline {
			continue
		}
		for _, a := range p.Args {
			if a.Secret {
				entry.SecretArgs = append(entry.SecretArgs, a.Name)
			}
		}
	}
	res, err := RunHosted(ctx, paths, opts, entry)
	if err != nil {
		// safety: past describe the pipeline is known, so a setup error is the engine's own and its text, which
		// may carry a store's or a secret source's detail, stays in this process's log; a config error is the user's.
		recorded := errors.New(pipelineSetupFailed)
		if configErr := (*projectConfigError)(nil); errors.As(err, &configErr) {
			recorded = err
		}
		return refuse(err, recorded)
	}
	logger.Info("run finished (local)", "run_id", res.RunID, "pipeline", trigger.Pipeline, "status", res.Status)
	return nil
}

// safety: a hosted trigger run renders nothing, as the child the dispatcher started did: its output would land
// between the parent run's records, and the run's own logs and envelope keep every record.
type quietRunLog struct{}

func (quietRunLog) Log(string, string)       {}
func (quietRunLog) Emit(sparkwing.LogRecord) {}

// safety: a child run picks its backend the way its parent did, so a hosted
// run dispatches children that open nothing either and an unhosted one hands
// its child the very store its trigger row lives in.
func localTriggerBackends(ctx context.Context, paths Paths, admission *LocalAdmission) (Backends, hostedSelection, func(), error) {
	opts := Options{DefaultStateDB: paths.StateDB(), Admission: admission}
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
