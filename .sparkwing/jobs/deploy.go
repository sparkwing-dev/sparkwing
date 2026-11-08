package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type DeployArgs struct {
	DryRun          bool   `flag:"dry-run" desc:"Print every action the deploy would take, in order, and take none of them."`
	Version         string `flag:"version" desc:"Version to build and tag the images as. Default: Go's canonical pseudo-version for main. A real deploy takes it only from sparkwing.yaml args."`
	StopAfter       string `flag:"stop-after" desc:"Stop after this stage: staging deploys and gates staging only. Default: promote to production. A real deploy takes it only from sparkwing.yaml args."`
	PromoteProven   bool   `flag:"promote-proven" desc:"Skip staging and promote to production on the staging-gate record of the last successful default-branch deploy run, which must name the same image digests. A real deploy takes it only from sparkwing.yaml args."`
	ControllerBase  string `flag:"controller-base" desc:"Controller runtime base by digest (build-images --runtime-base). Default: the qa.20 base."`
	RunnerBase      string `flag:"runner-base" desc:"Runner runtime base by digest (build-images --runtime-base). Default: the qa.20 base."`
	ProvenRecordAge string `flag:"proven-max-age" desc:"Oldest staging-gate record --promote-proven accepts, as a Go duration. Default: 168h."`
}

type Deploy struct {
	sparkwing.Base
}

func (Deploy) ShortHelp() string {
	return "Build candidate images, deploy staging, gate it, promote to production with a smoke and rollback"
}

func (Deploy) Help() string {
	return "Turns the Sparkwing Cloud deploy lanes into one DAG. build-images builds linux/amd64 and linux/arm64 binaries with bin/build-release-target.sh and appends them to the runtime bases with publish-images, " +
		"reusing images an earlier run already published for the same commit. staging-deploy and production pin the digests in sparkwing-gitops, render with the candidate chart and push the controller, then the launcher, " +
		"each after Argo reports Synced/Healthy and the deployment has rolled out. The controller's commit changes only the controller Deployment; every other chart change lands with the launcher. Recovery relies on RDS point-in-time recovery, with no deploy-time snapshot or backup preflight. staging-gate runs the example pipeline and the sparkwing-ci-smoke reference pipeline on staging Cloud. " +
		"production requires a staging-gate record for the same digests, from this run or, with --promote-proven, from the last successful default-branch deploy run, then runs the two-job deploy-smoke pipeline in the " +
		"production team that owns the source repository installation and checks one NodeClaim per run and WARN- and ERROR-free controller and launcher logs. A failed production deploy or smoke reverts this run's GitOps commits only when the controller schema did not increase; a schema upgrade retains candidate images and records a Bitwing finding requiring roll forward. " +
		"Take a manual snapshot before a risky migration. Each environment's deployed and previous digests are that job's output. Credentials come from this run's OIDC token: the sparkwing-deploy role reads the GitOps deploy key and the fixture tokens " +
		"from SSM; no key is stored with the pipeline. Only a run the GitHub App started for a push to main can assume that role, so --stop-after, --promote-proven and --version reach a real deploy only through args " +
		"in .sparkwing/sparkwing.yaml; on the command line they work with --dry-run, which prints every action without taking any. Every deploy job refuses a commit that is no longer the tip of origin/main, and " +
		"production refuses a version below the one it runs, so retrying an old run cannot roll production back."
}

func (Deploy) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Print every action a deploy of HEAD would take", Command: "sparkwing run deploy -- --dry-run"},
		{Comment: "Print a staging-only deploy", Command: "sparkwing run deploy -- --dry-run --stop-after staging"},
		{Comment: "Print a promotion of the last staging-proven images", Command: "sparkwing run deploy -- --dry-run --promote-proven"},
	}
}

var deployRuns = sparkwing.NewConcurrencyGroup("sparkwing-cloud-deploy", sparkwing.ConcurrencyLimit{Capacity: 1})

func (p *Deploy) Plan(_ context.Context, plan *sparkwing.Plan, in DeployArgs, rc sparkwing.RunContext) error {
	switch in.StopAfter {
	case "", "staging", "production":
	default:
		return fmt.Errorf("deploy: --stop-after must be staging or production, not %q", in.StopAfter)
	}
	if in.PromoteProven && in.StopAfter == "staging" {
		return errors.New("deploy: --promote-proven skips staging, so --stop-after staging leaves nothing to do")
	}
	maxAge := 7 * 24 * time.Hour
	if in.ProvenRecordAge != "" {
		d, err := time.ParseDuration(in.ProvenRecordAge)
		if err != nil || d <= 0 {
			return fmt.Errorf("deploy: --proven-max-age %q is not a positive duration", in.ProvenRecordAge)
		}
		maxAge = d
	}
	cfg := defaultDeployConfig()
	if in.ControllerBase != "" {
		cfg.ControllerBase = in.ControllerBase
	}
	if in.RunnerBase != "" {
		cfg.RunnerBase = in.RunnerBase
	}
	base := deployJob{Cfg: cfg, Dry: in.DryRun || rc.DryRun, RunID: rc.RunID}
	var runnerLabels []string
	if !base.Dry {
		plan.Concurrency(deployRuns)
		runnerLabels = []string{"deploy-kikd-prod"}
	}

	build := sparkwing.Job(plan, "build-images", &deployBuildJob{Job: base.named("build-images"), Version: in.Version}).
		Requires(runnerLabels...).Timeout(cfg.buildBudget())
	images := sparkwing.RefTo[deployImages](build)
	var gate *sparkwing.JobNode
	if in.PromoteProven {
		gate = sparkwing.Job(plan, "staging-gate", &deployProvenGateJob{
			Proven: sparkwing.RefToLastRun[stagingGateRecord]("deploy", "staging-gate", sparkwing.MaxAge(maxAge)),
			MaxAge: maxAge,
			Dry:    base.Dry,
		}).Inline().Requires(runnerLabels...)
	} else {
		staging := sparkwing.Job(plan, "staging-deploy", &deployStagingJob{Job: base.named("staging-deploy"), Images: images}).
			Needs(build).Requires(runnerLabels...).Timeout(cfg.stagingBudget())
		gate = sparkwing.Job(plan, "staging-gate", &deployGateJob{Job: base.named("staging-gate"), Images: images}).
			Needs(staging).Requires(runnerLabels...).Timeout(cfg.gateBudget())
		if in.StopAfter == "staging" {
			return nil
		}
	}
	prod := &deployProductionJob{Job: base.named("production"), Images: images, Gate: sparkwing.RefTo[stagingGateRecord](gate)}
	sparkwing.Job(plan, "production", prod).Needs(build, gate).Requires(runnerLabels...).Timeout(cfg.productionBudget())
	return nil
}

type deployJob struct {
	Cfg   deployConfig
	Dry   bool
	RunID string
	Name  string
	tools *deployTools
}

func (j deployJob) named(name string) deployJob {
	j.Name = name
	return j
}

// safety: a job killed past its budget leaves its directory, which may hold a
// key or token, so each job first removes deploy directories older than the
// longest budget, which no live job can own.
func (j *deployJob) deployer() (*deployer, func(), error) {
	tools := realDeployTools()
	if j.tools != nil {
		tools = *j.tools
	}
	if _, err := j.Cfg.toolDirInUse(j.Dry); err != nil {
		return nil, nil, err
	}
	if err := sweepStaleDeployDirs(os.TempDir(), tools.now(), j.Cfg.productionBudget()); err != nil {
		return nil, nil, err
	}
	tmp, err := os.MkdirTemp("", deployDirPrefix+j.RunID+"-"+j.Name+"-")
	if err != nil {
		return nil, nil, err
	}
	d := &deployer{tools: tools, cfg: j.Cfg, dry: j.Dry, runID: j.RunID, repoDir: sparkwing.WorkDir(), tmp: tmp}
	return d, func() {
		if err := removeDeployDir(tmp); err != nil {
			fmt.Fprintln(os.Stderr, "deploy: remove", tmp+":", err)
		}
	}, nil
}

// safety: a dry-run body only prints, so it is marked safe and runs under
// --sw-dry-run too, which lets its outputs feed the next job's printout.
func deployStep[T any](w *sparkwing.Work, dry bool, id string, fn func(context.Context) (T, error)) *sparkwing.WorkStep {
	step := sparkwing.Step(w, id, fn)
	if dry {
		step.SafeWithoutDryRun()
	}
	return step
}

type deployBuildJob struct {
	sparkwing.Base
	Job deployJob
	sparkwing.Produces[deployImages]
	Version string
}

func (j *deployBuildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return deployStep(w, j.Job.Dry, "build", func(ctx context.Context) (deployImages, error) {
		d, done, err := j.Job.deployer()
		if err != nil {
			return deployImages{}, err
		}
		defer done()
		images, err := d.buildCandidate(ctx, j.Version)
		if err == nil {
			sparkwing.Summary(ctx, fmt.Sprintf("Candidate %s from `%s`\n\n- controller `%s`\n- runner `%s`\n- store baseline `%s`",
				images.Version, images.Commit, images.Controller.ref(), images.Runner.ref(), images.SchemaDigest))
		}
		return images, err
	}), nil
}

type deployStagingJob struct {
	sparkwing.Base
	Job deployJob
	sparkwing.Produces[envDeployment]
	Images sparkwing.Ref[deployImages]
}

func (j *deployStagingJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return deployStep(w, j.Job.Dry, "deploy", func(ctx context.Context) (envDeployment, error) {
		d, done, err := j.Job.deployer()
		if err != nil {
			return envDeployment{}, err
		}
		defer done()
		rec, _, err := d.deployTo(ctx, d.cfg.Staging, j.Images.Get(ctx))
		if err == nil {
			summarizeDeployment(ctx, rec)
		}
		return rec, err
	}), nil
}

func (d *deployer) deployTo(ctx context.Context, env deployEnv, images deployImages) (envDeployment, map[string]string, error) {
	if err := d.requireMainTip(ctx, images.Commit); err != nil {
		return envDeployment{}, nil, err
	}
	if err := d.assumeRole(ctx); err != nil {
		return envDeployment{}, nil, err
	}
	if err := d.ecrLogin(ctx); err != nil {
		return envDeployment{}, nil, err
	}
	if err := d.kubeconfig(ctx); err != nil {
		return envDeployment{}, nil, err
	}
	var gitEnv map[string]string
	if err := d.act(ctx, "read the GitOps deploy key from "+d.cfg.Params.GitopsKey, func() error {
		var err error
		gitEnv, err = d.gitEnv(ctx, d.cfg.Params.GitopsKey, "gitops-key")
		return err
	}); err != nil {
		return envDeployment{}, nil, err
	}
	rec, err := d.deployEnvironment(ctx, env, images, gitEnv)
	return rec, gitEnv, err
}

func summarizeDeployment(ctx context.Context, rec envDeployment) {
	sparkwing.Summary(ctx, fmt.Sprintf("%s runs %s\n\n- controller `%s` (was `%s`)\n- runner `%s` (was `%s`)\n- GitOps changes: %d",
		rec.Environment, rec.Images.Version, rec.Images.Controller.Digest, rec.Previous.Controller.Digest,
		rec.Images.Runner.Digest, rec.Previous.Launcher.Digest, len(rec.Changes)))
}

type deployGateJob struct {
	sparkwing.Base
	Job deployJob
	sparkwing.Produces[stagingGateRecord]
	Images sparkwing.Ref[deployImages]
}

func (j *deployGateJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return deployStep(w, j.Job.Dry, "gate", func(ctx context.Context) (stagingGateRecord, error) {
		d, done, err := j.Job.deployer()
		if err != nil {
			return stagingGateRecord{}, err
		}
		defer done()
		return d.stagingGate(ctx, j.Images.Get(ctx))
	}), nil
}

// safety: the latest-run lookup reads only the newest successful deploy run,
// so a promotion re-emits the record it promoted on; the next promotion then
// finds that record instead of a run with no gate.
type deployProvenGateJob struct {
	sparkwing.Base
	sparkwing.Produces[stagingGateRecord]
	Proven sparkwing.Ref[stagingGateRecord]
	MaxAge time.Duration
	Dry    bool
}

func (j *deployProvenGateJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "recorded", func(ctx context.Context) (stagingGateRecord, error) {
		if j.Dry {
			sparkwing.Info(ctx, "dry-run: would read the staging-gate record of the last successful default-branch deploy run, no older than %s", j.MaxAge)
			return stagingGateRecord{RunID: "<recorded run>"}, nil
		}
		return provenGateRecord(ctx, j.Proven, time.Now(), j.MaxAge)
	}).SafeWithoutDryRun(), nil
}

// safety: a re-emitted record keeps the time its gate ran, so chaining
// promotions cannot stretch --proven-max-age past the staging pass itself.
func provenGateRecord(ctx context.Context, proven sparkwing.Ref[stagingGateRecord], now time.Time, maxAge time.Duration) (stagingGateRecord, error) {
	rec, ok := proven.TryGet(ctx)
	if !ok {
		return rec, errors.New("the last successful default-branch deploy run holds no staging-gate record; deploy staging first")
	}
	gated, err := time.Parse(time.RFC3339, rec.GatedAt)
	if err != nil {
		return rec, fmt.Errorf("the staging-gate record of run %s has no gate time: %w", rec.RunID, err)
	}
	if now.Sub(gated) > maxAge {
		return rec, fmt.Errorf("the staging gate of run %s ran at %s, longer ago than %s; deploy staging again", rec.RunID, rec.GatedAt, maxAge)
	}
	sparkwing.Info(ctx, "promoting on the staging-gate record of run %s", rec.RunID)
	return rec, nil
}

type deployProductionJob struct {
	sparkwing.Base
	Job deployJob
	sparkwing.Produces[envDeployment]
	Images sparkwing.Ref[deployImages]
	Gate   sparkwing.Ref[stagingGateRecord]
}

func (j *deployProductionJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return deployStep(w, j.Job.Dry, "promote", func(ctx context.Context) (envDeployment, error) {
		d, done, err := j.Job.deployer()
		if err != nil {
			return envDeployment{}, err
		}
		defer done()
		images := j.Images.Get(ctx)
		if err := d.act(ctx, "require a staging-gate pass for controller "+images.Controller.Digest+" and runner "+images.Runner.Digest, func() error {
			return requireStagingPass(ctx, images, j.Gate)
		}); err != nil {
			return envDeployment{}, err
		}
		rec, err := d.promote(ctx, images)
		if err == nil {
			summarizeDeployment(ctx, rec)
		}
		return rec, err
	}), nil
}

// safety: production takes only digests a staging gate passed, in this run or
// in the recorded run the staging-gate job re-emitted.
func requireStagingPass(ctx context.Context, images deployImages, gate sparkwing.Ref[stagingGateRecord]) error {
	rec, ok := gate.TryGet(ctx)
	if !ok {
		return errors.New("no staging-gate record; production needs a staging pass for these digests")
	}
	if !rec.Images.sameDigests(images) {
		return fmt.Errorf("the staging gate of run %s passed controller %s and runner %s, not %s and %s",
			rec.RunID, rec.Images.Controller.Digest, rec.Images.Runner.Digest, images.Controller.Digest, images.Runner.Digest)
	}
	return nil
}

func (d *deployer) promote(ctx context.Context, images deployImages) (envDeployment, error) {
	env := d.cfg.Production
	rec, gitEnv, err := d.deployTo(ctx, env, images)
	if err == nil {
		err = d.productionSmoke(ctx, images, rec.WindowStart)
	}
	if d.dry {
		d.tools.say(ctx, "dry-run: on a failed production deploy or smoke, would renew the role session and revert this run's GitOps commits newest first only if the controller schema did not increase; otherwise retain candidate images and record a Bitwing finding requiring roll forward")
	}
	if err == nil {
		return rec, nil
	}
	if len(rec.Changes) == 0 {
		return rec, err
	}
	if rec.CandidateSchema > rec.PreviousSchema || rec.PreviousSchema <= 0 || rec.CandidateSchema <= 0 {
		feedbackErr := d.recordUnsafeRollback(ctx, rec, err)
		if feedbackErr != nil {
			err = errors.Join(err, fmt.Errorf("bitwing rollback finding could not be recorded: %w", feedbackErr))
		}
		return rec, fmt.Errorf("automatic rollback disabled: controller schema %d -> %d; candidate images retained, production requires roll forward: %w", rec.PreviousSchema, rec.CandidateSchema, err)
	}
	d.tools.say(ctx, "production failed, rolling back: "+err.Error())
	if rbErr := d.rollbackDetached(ctx, env, rec, gitEnv); rbErr != nil {
		return rec, errors.Join(err, fmt.Errorf("rollback failed, production needs a person: %w", rbErr))
	}
	return rec, fmt.Errorf("production rolled back to controller %s and runner %s: %w", rec.Previous.Controller.Digest, rec.Previous.Launcher.Digest, err)
}

type DeploySmoke struct{ sparkwing.Base }

func (DeploySmoke) ShortHelp() string {
	return "Two-job Cloud smoke the deploy pipeline runs in the production source-owning team"
}

func (DeploySmoke) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	greet := sparkwing.Job(plan, "greet", func(ctx context.Context) error {
		sparkwing.Info(ctx, "deploy smoke: greet")
		return nil
	})
	sparkwing.Job(plan, "after", func(ctx context.Context) error {
		sparkwing.Info(ctx, "deploy smoke: after")
		return nil
	}).Needs(greet)
	return nil
}

func init() {
	sparkwing.Register[DeployArgs]("Deploy", func() sparkwing.Pipeline[DeployArgs] { return &Deploy{} })
	sparkwing.Register("DeploySmoke", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &DeploySmoke{} })
}
