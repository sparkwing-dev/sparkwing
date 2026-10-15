package settings

import (
	"fmt"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func planInputs(in PlanInputs) (Inputs, error) {
	if err := ValidateCheckoutDeclarations(in.Declarations); err != nil {
		return Inputs{}, err
	}
	code, err := planValues(in.Plan)
	if err != nil {
		return Inputs{}, err
	}
	ctx := in.Context
	ctx.Submitter = true
	ctx.Origin = in.Request.Request.Origin
	return Inputs{Sealed: unprojectOptions(in.Request.Request.Options), Pipeline: unprojectLayer(in.Declarations.Pipeline), Defaults: unprojectLayer(in.Declarations.Defaults), Code: code, Operator: unprojectCeilings(in.Request.Ceilings), Context: ctx}, nil
}

func resolveRun(in Inputs) (runspec.Options, error) {
	out := in.Sealed
	var err error
	if err = resolveValue(in, "priority", Priority, &out.Priority, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "admission_class", AdmissionClass, &out.AdmissionClass, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "claim_wait", ClaimWait, &out.ClaimWait, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "profile", Profile, &out.Profile, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveList(in, "requires", Requires, &out.Requires, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveList(in, "prefers", Prefers, &out.Prefers, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	resources, origins, origin, err := resourceLimits(in)
	if err != nil {
		return runspec.Options{}, err
	}
	if resources.Cores != nil || resources.MemoryBytes != nil {
		out.Resources = &resources
		mergeOrigins(out.Origins, origins)
		out.Origins["resources"] = origin
	}
	policy, origins, origin, err := checkout(in)
	if err != nil {
		return runspec.Options{}, err
	}
	out.Checkout = &policy
	mergeOrigins(out.Origins, origins)
	out.Origins["source"] = origin
	return ProjectRequest(out)
}

func jobInputs(base Inputs, job nodeproto.JobSpec) (Inputs, error) {
	in := base
	in.Code.Origins = map[string]Origin{}
	var err error
	if in.Code.JobTimeout, err = milliseconds(job.TimeoutMS, "job.timeout"); err != nil {
		return in, err
	}
	if in.Code.NoProgressTimeout, err = milliseconds(job.NoProgressTimeoutMS, "job.no_progress_timeout"); err != nil {
		return in, err
	}
	if job.Retry != nil {
		in.Code.Retry = pointer(job.Retry.Attempts)
		if in.Code.RetryBackoff, err = milliseconds(job.Retry.BackoffMS, "job.retry_backoff"); err != nil {
			return in, err
		}
	}
	if job.Memo != nil {
		if in.Code.MemoTTL, err = milliseconds(job.Memo.TTLMS, "job.memo_ttl"); err != nil {
			return in, err
		}
	}
	if job.Approval != nil {
		if in.Code.ApprovalWait, err = milliseconds(job.Approval.TimeoutMS, "approval.wait"); err != nil {
			return in, err
		}
	}
	if job.Resources != nil {
		resources := Resources{}
		if in.Code.Resources != nil {
			resources = *in.Code.Resources
		}
		if job.Resources.Cores != nil {
			resources.Cores = job.Resources.Cores
		}
		if job.Resources.MemoryBytes != nil {
			resources.MemoryBytes = job.Resources.MemoryBytes
		}
		in.Code.Resources = &resources
	}
	in.Code.Prefers = job.Prefers
	for _, key := range []string{"job.timeout", "job.no_progress_timeout", "job.retry", "job.retry_backoff", "job.memo_ttl", "approval.wait", "resources", "prefers"} {
		in.Code.Origins[key] = Origin{Source: Code, Key: "jobs." + job.ID + "." + key}
	}
	return in, nil
}

func resolveJob(base Inputs, job nodeproto.JobSpec, run runspec.Options) (runspec.JobExecutionSettings, error) {
	in, err := jobInputs(base, job)
	if err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	out := Values{Origins: map[string]Origin{}}
	if in.Code.JobTimeout != nil || in.Sealed.JobTimeout != nil {
		if err = resolveValue(in, "job.timeout", JobTimeout, &out.JobTimeout, out.Origins); err != nil {
			return runspec.JobExecutionSettings{}, err
		}
	}
	if err = resolveValue(in, "job.no_progress_timeout", NoProgressTimeout, &out.NoProgressTimeout, out.Origins); err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	if err = resolveValue(in, "job.retry", Retry, &out.Retry, out.Origins); err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	if err = resolveValue(in, "job.retry_backoff", RetryBackoff, &out.RetryBackoff, out.Origins); err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	if job.Memo != nil {
		if err = resolveValue(in, "job.memo_ttl", MemoTTL, &out.MemoTTL, out.Origins); err != nil {
			return runspec.JobExecutionSettings{}, err
		}
	}
	if job.Approval != nil {
		if err = resolveValue(in, "approval.wait", ApprovalWait, &out.ApprovalWait, out.Origins); err != nil {
			return runspec.JobExecutionSettings{}, err
		}
	}
	resources, origins, origin, err := resourceLimits(in)
	if err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	if resources.Cores != nil || resources.MemoryBytes != nil {
		out.Resources = &resources
		mergeOrigins(out.Origins, origins)
		out.Origins["resources"] = origin
	}
	out.Requires = slices.Clone(run.Requires.Value)
	for _, requirement := range job.Requires {
		if !slices.Contains(out.Requires, requirement) {
			out.Requires = append(out.Requires, requirement)
		}
	}
	if out.Requires == nil {
		out.Requires = []string{}
	}
	out.Origins["requires"] = fromOrigin(run.Requires.Origin)
	if err = validNames("requires", out.Requires, out.Origins["requires"]); err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	if err = resolveList(in, "prefers", Prefers, &out.Prefers, out.Origins); err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	projected, err := ProjectRequest(out)
	if err != nil {
		return runspec.JobExecutionSettings{}, err
	}
	result := projected.JobDefaults
	result.Requires = projected.Requires
	result.Prefers = projected.Prefers
	result.Resources = projected.Resources
	return result, nil
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
	options, err := resolveRun(inputs)
	if err != nil {
		return runspec.ExecutionSettings{}, err
	}
	jobs, err := resolveJobs(inputs, in.Plan.Jobs, options)
	if err != nil {
		return runspec.ExecutionSettings{}, err
	}
	return runspec.ExecutionSettings{Options: options, Jobs: jobs}, nil
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
	accepted := unprojectOptions(in.Spec.Resolved.Settings.Options)
	plan.Priority = accepted.Priority
	plan.AdmissionClass = accepted.AdmissionClass
	plan.Source = accepted.Checkout
	plan.Resources = nil
	if accepted.Resources != nil {
		plan.Resources = &nodeproto.ResourceSpec{Cores: accepted.Resources.Cores, MemoryBytes: accepted.Resources.MemoryBytes}
	}
	plan.ClaimWaitMS = nil
	if accepted.ClaimWait != nil {
		plan.ClaimWaitMS = pointer(accepted.ClaimWait.Milliseconds())
	}
	inputs, err := planInputs(PlanInputs{Request: in.Spec.Request, Declarations: in.Spec.Declarations, Plan: plan, Context: in.Context})
	if err != nil {
		return nil, err
	}
	return resolveJobs(inputs, plan.Jobs, in.Spec.Resolved.Settings.Options)
}
