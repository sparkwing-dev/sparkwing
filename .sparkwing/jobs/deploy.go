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
	Version         string `flag:"version" desc:"Version to build and tag the images as. Default: the release tag at HEAD, else the next patch version plus -main.g<commit>."`
	StopAfter       string `flag:"stop-after" desc:"Stop after this stage: staging deploys and gates staging only. Default: promote to production."`
	PromoteProven   bool   `flag:"promote-proven" desc:"Skip staging and promote to production on the staging-gate record of the last successful default-branch deploy run, which must name the same image digests."`
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
	return "Turns the manual Sparkwing Cloud release lanes into one DAG. build-images builds linux/amd64 and linux/arm64 binaries with bin/build-release-target.sh and appends them to the runtime bases with publish-images, " +
		"reusing images an earlier run already published for the same commit. staging-deploy and production-deploy pin the digests in kikd-gitops, render with the candidate chart, push the controller and wait for Argo " +
		"Synced/Healthy and its rollout before pushing the launcher. Each refuses when the environment's store baseline differs from the candidate's, because a store change takes the full gate. staging-gate runs the " +
		"example pipeline and the sparkwing-ci-smoke reference pipeline on staging Cloud. production-deploy requires a staging-gate record for the same digests, from this run or, with --promote-proven, from the last " +
		"successful default-branch deploy run, then runs the two-job deploy-smoke pipeline in each QA fixture team and checks one NodeClaim per run and WARN- and ERROR-free controller and launcher logs. A failed " +
		"production deploy or smoke reverts the run's GitOps commits, launcher first. Each environment's deployed and previous digests are that job's output. Credentials come from this run's OIDC token: the " +
		"sparkwing-deploy role reads the GitOps deploy key and the fixture tokens from SSM; no key is stored with the pipeline. --dry-run prints every action without taking any."
}

func (Deploy) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Print every action a deploy of HEAD would take", Command: "sparkwing run deploy -- --dry-run"},
		{Comment: "Deploy and gate staging only", Command: "sparkwing run deploy -- --stop-after staging"},
		{Comment: "Promote the last staging-proven images", Command: "sparkwing run deploy -- --promote-proven"},
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
	if !base.Dry {
		plan.Concurrency(deployRuns)
	}

	build := sparkwing.Job(plan, "build-images", &deployBuildJob{Job: base, Version: in.Version}).Timeout(45 * time.Minute)
	images := sparkwing.RefTo[deployImages](build)
	prod := &deployProductionJob{Job: base, Images: images}
	if in.PromoteProven {
		prod.Proven = sparkwing.RefToLastRun[stagingGateRecord]("deploy", "staging-gate", sparkwing.MaxAge(maxAge))
	} else {
		staging := sparkwing.Job(plan, "staging-deploy", &deployStagingJob{Job: base, Images: images}).
			Needs(build).Timeout(30 * time.Minute)
		gate := sparkwing.Job(plan, "staging-gate", &deployGateJob{Job: base, Images: images}).
			Needs(staging).Timeout(90 * time.Minute)
		if in.StopAfter == "staging" {
			return nil
		}
		prod.Gate = sparkwing.RefTo[stagingGateRecord](gate)
	}
	sparkwing.Job(plan, "production", prod).Needs(build).Timeout(90 * time.Minute)
	return nil
}

type deployJob struct {
	Cfg   deployConfig
	Dry   bool
	RunID string
	tools *deployTools
}

func (j *deployJob) deployer() (*deployer, func(), error) {
	tools := realDeployTools()
	if j.tools != nil {
		tools = *j.tools
	}
	tmp, err := os.MkdirTemp("", "sparkwing-deploy-")
	if err != nil {
		return nil, nil, err
	}
	d := &deployer{tools: tools, cfg: j.Cfg, dry: j.Dry, runID: j.RunID, repoDir: sparkwing.WorkDir(), tmp: tmp}
	return d, func() {
		if err := os.RemoveAll(tmp); err != nil {
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

type deployProductionJob struct {
	sparkwing.Base
	Job deployJob
	sparkwing.Produces[envDeployment]
	Images sparkwing.Ref[deployImages]
	Gate   sparkwing.Ref[stagingGateRecord]
	Proven sparkwing.Ref[stagingGateRecord]
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
			return requireStagingPass(ctx, images, j.Gate, j.Proven)
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

// safety: production takes only digests a staging gate passed, from this run
// or from the recorded last successful default-branch deploy run.
func requireStagingPass(ctx context.Context, images deployImages, gate, proven sparkwing.Ref[stagingGateRecord]) error {
	rec, ok := stagingGateRecord{}, false
	source := "this run's staging-gate"
	if gate.JobID != "" {
		rec, ok = gate.TryGet(ctx)
	} else if proven.Pipeline != "" {
		source = "the last successful deploy run's staging-gate"
		rec, ok = proven.TryGet(ctx)
	}
	if !ok {
		return fmt.Errorf("no staging-gate record from %s; production needs a staging pass for these digests", source)
	}
	if !rec.Images.sameDigests(images) {
		return fmt.Errorf("%s (run %s) passed controller %s and runner %s, not %s and %s",
			source, rec.RunID, rec.Images.Controller.Digest, rec.Images.Runner.Digest, images.Controller.Digest, images.Runner.Digest)
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
		d.tools.say(ctx, "dry-run: on a failed production deploy or smoke, would revert this run's GitOps commits newest first and wait for each rollout")
	}
	if err == nil {
		return rec, nil
	}
	if len(rec.Changes) == 0 {
		return rec, err
	}
	d.tools.say(ctx, "production failed, rolling back: "+err.Error())
	if rbErr := d.rollback(ctx, env, rec, gitEnv); rbErr != nil {
		return rec, errors.Join(err, fmt.Errorf("rollback failed, production needs a person: %w", rbErr))
	}
	return rec, fmt.Errorf("production rolled back to controller %s and runner %s: %w", rec.Previous.Controller.Digest, rec.Previous.Launcher.Digest, err)
}

type DeploySmoke struct{ sparkwing.Base }

func (DeploySmoke) ShortHelp() string {
	return "Two-job Cloud smoke the deploy pipeline runs in the production QA teams"
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
