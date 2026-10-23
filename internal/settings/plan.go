package settings

import (
	"fmt"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func planInputs(in PlanInputs) (Inputs, error) {
	if err := ValidateCheckoutDeclarations(in.Declarations); err != nil {
		return Inputs{}, err
	}
	code, err := planOptions(in.Plan)
	if err != nil {
		return Inputs{}, err
	}
	ctx := in.Context
	ctx.Submitter = true
	ctx.Origin = in.Request.Request.Origin
	return Inputs{Sealed: Values{Options: in.Request.Request.Options}, Pipeline: Values{Options: layerOptions(in.Declarations.Pipeline)}, Defaults: Values{Options: layerOptions(in.Declarations.Defaults)}, Code: Values{Options: code}, ceilings: &in.Request.Ceilings, Context: ctx}, nil
}

func resolveRun(in Inputs, out runspec.Options) (runspec.Options, error) {
	var err error
	if err = resolveSetting(in, Priority, &out.Priority); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, AdmissionClass, &out.AdmissionClass); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, ClaimWait, &out.ClaimWait); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, Profile, &out.Profile); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, Requires, &out.Requires); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, Prefers, &out.Prefers); err != nil {
		return runspec.Options{}, err
	}
	out.Checkout, err = checkoutSetting(in)
	return out, err
}

func layerOptions(layer runspec.CheckoutLayer) runspec.Options {
	return runspec.Options{Requires: layer.Requires, Prefers: layer.Prefers, Profile: layer.Profile}
}

func codeValue[T any](value *T, key string) runspec.Setting[T] {
	if value == nil {
		return runspec.Setting[T]{}
	}
	return declaration(*value, runspec.SettingOrigin{Layer: runspec.LayerCode, Key: key})
}

func codeDuration(value *int64, key string) (runspec.Setting[time.Duration], error) {
	duration, err := milliseconds(value, key)
	return codeValue(duration, key), err
}

func planOptions(plan nodeproto.PlanDoc) (runspec.Options, error) {
	out := runspec.Options{Priority: codeValue(plan.Priority, "priority"), AdmissionClass: codeValue(plan.AdmissionClass, "admission_class")}
	if plan.Source != nil {
		out.Checkout = sourceSetting(*plan.Source, Origin{Source: Code, Key: "source"})
	}
	var err error
	out.ClaimWait, err = codeDuration(plan.ClaimWaitMS, "claim_wait")
	return out, err
}

func jobInputs(base Inputs, job nodeproto.JobSpec) (Inputs, error) {
	in := base
	code := &in.Code.Options
	prefix := "jobs." + job.ID + "."
	var err error
	if code.JobDefaults.Timeout, err = codeDuration(job.TimeoutMS, prefix+"job.timeout"); err != nil {
		return in, err
	}
	if code.JobDefaults.NoProgressTimeout, err = codeDuration(job.NoProgressTimeoutMS, prefix+"job.no_progress_timeout"); err != nil {
		return in, err
	}
	if job.Retry != nil {
		code.JobDefaults.Retry = codeValue(&job.Retry.Attempts, prefix+"job.retry")
		if code.JobDefaults.RetryBackoff, err = codeDuration(job.Retry.BackoffMS, prefix+"job.retry_backoff"); err != nil {
			return in, err
		}
	}
	if job.Memo != nil {
		if code.JobDefaults.MemoTTL, err = codeDuration(job.Memo.TTLMS, prefix+"job.memo_ttl"); err != nil {
			return in, err
		}
	}
	if job.Approval != nil {
		if code.JobDefaults.ApprovalWait, err = codeDuration(job.Approval.TimeoutMS, prefix+"approval.wait"); err != nil {
			return in, err
		}
	}
	code.Prefers = runspec.Setting[[]string]{}
	if job.Prefers != nil {
		code.Prefers = codeValue(&job.Prefers, prefix+"prefers")
	}
	return in, nil
}

func resolveJob(base Inputs, job nodeproto.JobSpec, run runspec.Options) (runspec.JobExecutionSettings, error) {
	in, err := jobInputs(base, job)
	if err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	out := runspec.Options{}
	if in.options(Code).JobDefaults.Timeout.Set || in.options(Run).JobDefaults.Timeout.Set {
		if err = resolveSetting(in, JobTimeout, &out.JobDefaults.Timeout); err != nil {
			return runspec.JobExecutionSettings{}, err
		}
	}
	if err = resolveSetting(in, NoProgressTimeout, &out.JobDefaults.NoProgressTimeout); err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	if err = resolveSetting(in, Retry, &out.JobDefaults.Retry); err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	if err = resolveSetting(in, RetryBackoff, &out.JobDefaults.RetryBackoff); err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	if job.Memo != nil {
		if err = resolveSetting(in, MemoTTL, &out.JobDefaults.MemoTTL); err != nil {
			return runspec.JobExecutionSettings{}, err
		}
	}
	if job.Approval != nil {
		if err = resolveSetting(in, ApprovalWait, &out.JobDefaults.ApprovalWait); err != nil {
			return runspec.JobExecutionSettings{}, err
		}
	}
	out.Requires = run.Requires
	out.Requires.Value = nodeproto.NormalizeLabels(append(slices.Clone(run.Requires.Value), job.Requires...))
	if out.Requires.Value == nil {
		out.Requires.Value = []string{}
	}
	if err = validNames("requires", out.Requires.Value, fromOrigin(out.Requires.Origin)); err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	if err = resolveSetting(in, Prefers, &out.Prefers); err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	result := out.JobDefaults
	result.Requires = out.Requires
	result.Prefers = out.Prefers
	result.Concurrency, err = ResolveConcurrency(job.Concurrency)
	return result, err
}

// ResolvePlan returns every job winner keyed by declared job ID after C3 validation.
// It reapplies request precedence and sealed ceilings without changing either seal.
// C1 calls it and secret preflight inside AcceptPlan before writing any accepted state.
// It validates Declarations and derives winners from separate pipeline/default layers plus
// decoded plan declarations. It never accepts a planner-supplied resolved winner as authority.
func ResolvePlan(in PlanInputs) (runspec.ExecutionSettings, error) {
	inputs, err := planInputs(in)
	if err != nil {
		return runspec.ExecutionSettings{}, err
	}
	options, err := resolveRun(inputs, cloneOptions(in.Request.Request.Options))
	if err != nil {
		return runspec.ExecutionSettings{}, err
	}
	jobs, err := resolveJobs(inputs, in.Plan.Jobs, options)
	if err != nil {
		return runspec.ExecutionSettings{}, err
	}
	concurrency, err := ResolveConcurrency(in.Plan.Concurrency)
	if err != nil {
		return runspec.ExecutionSettings{}, err
	}
	result := runspec.ExecutionSettings{Options: options, Jobs: jobs, Concurrency: concurrency}
	if err := checkConcurrencyGroups(result); err != nil {
		return runspec.ExecutionSettings{}, err
	}
	return result, nil
}

func resolveJobs(in Inputs, jobs []nodeproto.JobSpec, options runspec.Options) (map[string]runspec.JobExecutionSettings, error) {
	out := make(map[string]runspec.JobExecutionSettings, len(jobs))
	for _, job := range jobs {
		if job.ID == "" {
			return nil, fmt.Errorf("job ID must not be empty")
		}
		if _, exists := out[job.ID]; exists {
			return nil, fmt.Errorf("duplicate job ID")
		}
		settings, err := resolveJob(in, job, options)
		if err != nil {
			return nil, err
		}
		out[job.ID] = settings
	}
	return out, nil
}

// ResolveExpansion resolves generated declarations using Spec.Declarations, accepted run settings
// and the server-owned sealed request/ceilings. It never rereads a checkout or host preferences.
// C1 persists these winners alongside each declaration/hash.
func ResolveExpansion(in ExpansionInputs) (map[string]runspec.JobExecutionSettings, error) {
	plan := in.Plan
	inputs, err := planInputs(PlanInputs{Request: in.Spec.Request, Declarations: in.Spec.Declarations, Plan: plan, Context: in.Context})
	if err != nil {
		return nil, err
	}
	accepted := in.Spec.Resolved.Settings.Options
	inputs.Code.Priority = accepted.Priority
	inputs.Code.AdmissionClass = accepted.AdmissionClass
	inputs.Code.Checkout = accepted.Checkout
	inputs.Code.ClaimWait = accepted.ClaimWait
	jobs, err := resolveJobs(inputs, plan.Jobs, in.Spec.Resolved.Settings.Options)
	if err != nil {
		return nil, err
	}
	combined := in.Spec.Resolved.Settings
	combined.Jobs = make(map[string]runspec.JobExecutionSettings, len(jobs)+len(in.Spec.Resolved.Settings.Jobs))
	for id, settings := range in.Spec.Resolved.Settings.Jobs {
		combined.Jobs[id] = settings
	}
	for id, settings := range jobs {
		combined.Jobs[id] = settings
	}
	if err := checkConcurrencyGroups(combined); err != nil {
		return nil, err
	}
	return jobs, nil
}
