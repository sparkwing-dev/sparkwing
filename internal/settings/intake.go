package settings

import (
	"maps"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// ResolveIntake resolves captured request preferences against trusted controller inputs.
func ResolveIntake(options runspec.Options, operator OperatorInputs, ctx Context) (runspec.Options, error) {
	for _, origin := range intakeOrigins(options) {
		switch origin.Layer {
		case "", runspec.LayerFlag, runspec.LayerEnv, runspec.LayerSealed, runspec.LayerMachine, runspec.LayerDefault:
		default:
			return runspec.Options{}, invalid(origin.Key, Origin{Source: Run, Key: origin.Key}, "request setting has an invalid intake origin")
		}
	}
	return ResolveRequest(Inputs{Sealed: unprojectOptions(options), Operator: operator, Context: ctx})
}

func resolveValue[T any](in Inputs, key string, resolver func(Inputs) (T, Origin, error), field **T, origins map[string]Origin) error {
	value, origin, err := resolver(in)
	if err != nil {
		return err
	}
	*field = &value
	origins[key] = origin
	return nil
}

func resolveList(in Inputs, key string, resolver func(Inputs) ([]string, Origin, error), field *[]string, origins map[string]Origin) error {
	value, origin, err := resolver(in)
	if err != nil {
		return err
	}
	*field = value
	origins[key] = origin
	return nil
}

// ResolveRequest resolves only intake inputs; project and code fields remain
// unset and their winning request overrides remain distinguishable from defaults.
// The store owns sealing and write-once enforcement in runspec.Request.
func ResolveRequest(in Inputs) (runspec.Options, error) {
	in.Context.Submitter = true
	in.Pipeline = Values{}
	in.Defaults = Values{}
	in.Code = Values{}
	out := Values{Origins: map[string]Origin{}}
	var err error
	if err = resolveValue(in, "runner", RunnerSelection, &out.RunnerSelection, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "owner_execution", OwnerExecution, &out.OwnerExecution, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "workers", Workers, &out.Workers, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "output", Output, &out.Output, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "start_at", StartAt, &out.StartAt, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "stop_at", StopAt, &out.StopAt, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "only", Only, &out.Only, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "dry_run", DryRun, &out.DryRun, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "no_memo", NoMemo, &out.NoMemo, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveList(in, "allow_risk", AllowRisk, &out.AllowRisk, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	pause, pauseOrigins, pauseOrigin, err := pauseTargets(in)
	if err != nil {
		return runspec.Options{}, err
	}
	out.Pause = &pause
	out.Origins["debug.pause"] = pauseOrigin
	mergeOrigins(out.Origins, pauseOrigins)
	if err = resolveValue(in, "debug.pause_timeout", PauseTimeout, &out.PauseTimeout, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "debug", Debug, &out.Debug, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "publish", Publish, &out.Publish, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveValue(in, "cache_from", CacheFrom, &out.CacheFrom, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveList(in, "run.submit_env_allow", SubmitEnvAllow, &out.SubmitEnvAllow, out.Origins); err != nil {
		return runspec.Options{}, err
	}
	if in.Flags.RepoLocation != nil || in.Sealed.RepoLocation != nil || in.Machine.RepoLocation != nil || in.Context.ParentCheckout != "" || len(in.Context.RegisteredCheckouts) > 0 || len(in.Context.FallbackPaths) > 0 {
		if err = resolveValue(in, "repo_location", RepoLocation, &out.RepoLocation, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Flags.Priority != nil || in.Sealed.Priority != nil {
		if err = resolveValue(in, "priority", Priority, &out.Priority, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Flags.Profile != nil || in.Sealed.Profile != nil {
		if err = resolveValue(in, "profile", Profile, &out.Profile, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Flags.Requires != nil || in.Sealed.Requires != nil {
		if err = resolveList(in, "requires", Requires, &out.Requires, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Flags.Prefers != nil || in.Sealed.Prefers != nil {
		if err = resolveList(in, "prefers", Prefers, &out.Prefers, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Sealed.AdmissionClass != nil {
		if err = resolveValue(in, "admission_class", AdmissionClass, &out.AdmissionClass, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Sealed.ClaimWait != nil {
		if err = resolveValue(in, "claim_wait", ClaimWait, &out.ClaimWait, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Sealed.Retry != nil {
		if err = resolveValue(in, "job.retry", Retry, &out.Retry, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Sealed.JobTimeout != nil {
		if err = resolveValue(in, "job.timeout", JobTimeout, &out.JobTimeout, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Sealed.NoProgressTimeout != nil {
		if err = resolveValue(in, "job.no_progress_timeout", NoProgressTimeout, &out.NoProgressTimeout, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Sealed.ApprovalWait != nil {
		if err = resolveValue(in, "approval.wait", ApprovalWait, &out.ApprovalWait, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Sealed.RetryBackoff != nil {
		if err = resolveValue(in, "job.retry_backoff", RetryBackoff, &out.RetryBackoff, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Sealed.MemoTTL != nil {
		if err = resolveValue(in, "job.memo_ttl", MemoTTL, &out.MemoTTL, out.Origins); err != nil {
			return runspec.Options{}, err
		}
	}
	if in.Sealed.Resources != nil {
		resources, origins, origin, err := resourceLimits(in)
		if err != nil {
			return runspec.Options{}, err
		}
		out.Resources = &resources
		mergeOrigins(out.Origins, origins)
		out.Origins["resources"] = origin
	}
	if in.Sealed.Checkout != nil {
		if err := validateSource(*in.Sealed.Checkout, false); err != nil {
			return runspec.Options{}, invalid("source", originFor(in.Sealed, Run, "source"), err.Error())
		}
		out.Checkout = pointer(cloneSource(*in.Sealed.Checkout))
		out.Origins["source"] = originFor(in.Sealed, Run, "source")
		for _, field := range []string{"full_history", "depth", "tags", "submodules", "lfs"} {
			if origin, ok := in.Sealed.Origins["source."+field]; ok {
				out.Origins["source."+field] = origin
			}
		}
	}
	return ProjectRequest(out)
}

func unprojectValue[T any](setting runspec.Setting[T], field **T, key string, origins map[string]Origin) {
	if setting.Set {
		*field = pointer(setting.Value)
		origins[key] = fromOrigin(setting.Origin)
	}
}

func unprojectList(setting runspec.Setting[[]string], field *[]string, key string, origins map[string]Origin) {
	if setting.Set {
		*field = slices.Clone(setting.Value)
		origins[key] = fromOrigin(setting.Origin)
	}
}

func unprojectResources(setting runspec.Setting[runspec.Resources], out *Values) {
	if !setting.Set {
		return
	}
	out.Resources = &Resources{}
	out.Origins["resources"] = fromOrigin(setting.Origin)
	unprojectValue(setting.Value.Cores, &out.Resources.Cores, "resources.cores", out.Origins)
	unprojectValue(setting.Value.MemoryBytes, &out.Resources.MemoryBytes, "resources.memory_bytes", out.Origins)
}

func unprojectOptions(options runspec.Options) Values {
	out := Values{Origins: map[string]Origin{}}
	unprojectValue(options.Runner, &out.RunnerSelection, "runner", out.Origins)
	unprojectValue(options.Priority, &out.Priority, "priority", out.Origins)
	unprojectValue(options.AdmissionClass, &out.AdmissionClass, "admission_class", out.Origins)
	unprojectValue(options.ClaimWait, &out.ClaimWait, "claim_wait", out.Origins)
	unprojectValue(options.OwnerExecutionTimeout, &out.OwnerExecution, "owner_execution", out.Origins)
	unprojectList(options.Requires, &out.Requires, "requires", out.Origins)
	unprojectList(options.Prefers, &out.Prefers, "prefers", out.Origins)
	unprojectValue(options.StartAt, &out.StartAt, "start_at", out.Origins)
	unprojectValue(options.StopAt, &out.StopAt, "stop_at", out.Origins)
	unprojectValue(options.Only, &out.Only, "only", out.Origins)
	unprojectValue(options.DryRun, &out.DryRun, "dry_run", out.Origins)
	unprojectValue(options.NoMemo, &out.NoMemo, "no_memo", out.Origins)
	unprojectList(options.AllowRisk, &out.AllowRisk, "allow_risk", out.Origins)
	unprojectValue(options.Workers, &out.Workers, "workers", out.Origins)
	unprojectValue(options.Output, &out.Output, "output", out.Origins)
	unprojectValue(options.PauseTimeout, &out.PauseTimeout, "debug.pause_timeout", out.Origins)
	unprojectValue(options.Debug, &out.Debug, "debug", out.Origins)
	unprojectValue(options.Publish, &out.Publish, "publish", out.Origins)
	unprojectValue(options.CacheFrom, &out.CacheFrom, "cache_from", out.Origins)
	unprojectList(options.SubmitEnvAllow, &out.SubmitEnvAllow, "run.submit_env_allow", out.Origins)
	unprojectValue(options.RepoLocation, &out.RepoLocation, "repo_location", out.Origins)
	unprojectValue(options.Profile, &out.Profile, "profile", out.Origins)
	unprojectValue(options.JobDefaults.Timeout, &out.JobTimeout, "job.timeout", out.Origins)
	unprojectValue(options.JobDefaults.NoProgressTimeout, &out.NoProgressTimeout, "job.no_progress_timeout", out.Origins)
	unprojectValue(options.JobDefaults.ApprovalWait, &out.ApprovalWait, "approval.wait", out.Origins)
	unprojectValue(options.JobDefaults.Retry, &out.Retry, "job.retry", out.Origins)
	unprojectValue(options.JobDefaults.RetryBackoff, &out.RetryBackoff, "job.retry_backoff", out.Origins)
	unprojectValue(options.JobDefaults.MemoTTL, &out.MemoTTL, "job.memo_ttl", out.Origins)
	unprojectResources(options.Resources, &out)
	if options.Checkout.Set {
		out.Checkout = pointer(cloneSource(options.Checkout.Value.Policy))
		out.Origins["source"] = fromOrigin(options.Checkout.Origin)
		for field, origin := range options.Checkout.Value.Origins {
			out.Origins["source."+field] = fromOrigin(origin)
		}
	}
	if options.Pause.Set {
		out.Pause = &PauseSelection{}
		out.Origins["debug.pause"] = fromOrigin(options.Pause.Origin)
		unprojectList(options.Pause.Value.Before, &out.Pause.Before, "debug.pause.before", out.Origins)
		unprojectList(options.Pause.Value.After, &out.Pause.After, "debug.pause.after", out.Origins)
		unprojectValue(options.Pause.Value.OnFailure, &out.Pause.OnFailure, "debug.pause.on_failure", out.Origins)
	}
	return out
}

func unprojectLayer(layer runspec.CheckoutLayer) Values {
	out := Values{Origins: map[string]Origin{}}
	if layer.Args.Set {
		out.Args = map[string]string{}
		out.Origins["args"] = fromOrigin(layer.Args.Origin)
		for key, arg := range layer.Args.Value {
			out.Args[key] = arg.Value
			out.Origins["args."+key] = fromOrigin(arg.Origin)
		}
	}
	if layer.Guards.Set {
		out.Guards = &projectconfig.Guards{When: slices.Clone(layer.Guards.Value.When), Unless: slices.Clone(layer.Guards.Value.Unless)}
		out.Origins["guards"] = fromOrigin(layer.Guards.Origin)
	}
	unprojectList(layer.Requires, &out.Requires, "requires", out.Origins)
	unprojectList(layer.Prefers, &out.Prefers, "prefers", out.Origins)
	unprojectValue(layer.Profile, &out.Profile, "profile", out.Origins)
	return out
}

func unprojectCeilings(ceilings runspec.Ceilings) OperatorInputs {
	out := OperatorValues{Origins: map[string]Origin{}}
	unprojectValue(ceilings.OwnerExecutionTimeout, &out.OwnerExecution, "operator.owner_execution", out.Origins)
	unprojectValue(ceilings.Priority, &out.MaxPriority, "operator.max_priority", out.Origins)
	unprojectValue(ceilings.ClaimWait, &out.MaxClaimWait, "operator.max_claim_wait", out.Origins)
	unprojectList(ceilings.AllowedAdmissionClasses, &out.AllowedAdmissionClasses, "operator.allowed_admission_classes", out.Origins)
	if ceilings.AllowedSources.Set {
		out.AllowedSources = slices.Clone(ceilings.AllowedSources.Value)
		out.Origins["operator.allowed_sources"] = fromOrigin(ceilings.AllowedSources.Origin)
	}
	if ceilings.Resources.Set {
		unprojectValue(ceilings.Resources.Value.Cores, &out.MaxCores, "operator.max_cores", out.Origins)
		unprojectValue(ceilings.Resources.Value.MemoryBytes, &out.MaxMemoryBytes, "operator.max_memory_bytes", out.Origins)
	}
	return distributeOperator(out)
}

func planValues(plan nodeproto.PlanDoc) (Values, error) {
	out := Values{Priority: plan.Priority, AdmissionClass: plan.AdmissionClass, Checkout: plan.Source, Origins: map[string]Origin{}}
	if plan.Resources != nil {
		out.Resources = &Resources{Cores: plan.Resources.Cores, MemoryBytes: plan.Resources.MemoryBytes}
	}
	var err error
	out.ClaimWait, err = milliseconds(plan.ClaimWaitMS, "claim_wait")
	if err != nil {
		return out, err
	}
	return out, nil
}

func milliseconds(value *int64, key string) (*time.Duration, error) {
	if value == nil {
		return nil, nil
	}
	if *value < 0 || *value > int64((1<<63-1)/time.Millisecond) {
		return nil, invalid(key, Origin{Source: Code, Key: key}, "duration is negative or overflows")
	}
	return pointer(time.Duration(*value) * time.Millisecond), nil
}

func mergeOrigins(dst, src map[string]Origin) { maps.Copy(dst, src) }

func distributeOperator(values OperatorValues) OperatorInputs {
	out := OperatorInputs{}
	for _, layer := range []struct {
		source Source
		target *OperatorValues
	}{{Flag, &out.Flags}, {TeamDB, &out.Team}, {GlobalDB, &out.Global}, {LimitsProfile, &out.Profile}} {
		layer.target.Origins = map[string]Origin{}
		if origin := values.Origins["operator.owner_execution"]; origin.Source == layer.source {
			layer.target.OwnerExecution = values.OwnerExecution
			layer.target.Origins["operator.owner_execution"] = origin
		}
		if origin := values.Origins["operator.max_cores"]; origin.Source == layer.source {
			layer.target.MaxCores = values.MaxCores
			layer.target.Origins["operator.max_cores"] = origin
		}
		if origin := values.Origins["operator.max_memory_bytes"]; origin.Source == layer.source {
			layer.target.MaxMemoryBytes = values.MaxMemoryBytes
			layer.target.Origins["operator.max_memory_bytes"] = origin
		}
		if origin := values.Origins["operator.max_priority"]; origin.Source == layer.source {
			layer.target.MaxPriority = values.MaxPriority
			layer.target.Origins["operator.max_priority"] = origin
		}
		if origin := values.Origins["operator.allowed_admission_classes"]; origin.Source == layer.source {
			layer.target.AllowedAdmissionClasses = values.AllowedAdmissionClasses
			layer.target.Origins["operator.allowed_admission_classes"] = origin
		}
		if origin := values.Origins["operator.allowed_sources"]; origin.Source == layer.source {
			layer.target.AllowedSources = values.AllowedSources
			layer.target.Origins["operator.allowed_sources"] = origin
		}
		if origin := values.Origins["operator.max_claim_wait"]; origin.Source == layer.source {
			layer.target.MaxClaimWait = values.MaxClaimWait
			layer.target.Origins["operator.max_claim_wait"] = origin
		}
	}
	return out
}

func intakeOrigins(options runspec.Options) []runspec.SettingOrigin {
	origins := []runspec.SettingOrigin{
		options.Runner.Origin,
		options.Priority.Origin,
		options.AdmissionClass.Origin,
		options.ClaimWait.Origin,
		options.OwnerExecutionTimeout.Origin,
		options.Workers.Origin,
		options.Output.Origin,
		options.StartAt.Origin,
		options.StopAt.Origin,
		options.Only.Origin,
		options.DryRun.Origin,
		options.NoMemo.Origin,
		options.PauseTimeout.Origin,
		options.Debug.Origin,
		options.Publish.Origin,
		options.CacheFrom.Origin,
		options.RepoLocation.Origin,
		options.Profile.Origin,
		options.JobDefaults.Timeout.Origin,
		options.JobDefaults.NoProgressTimeout.Origin,
		options.JobDefaults.ApprovalWait.Origin,
		options.JobDefaults.Retry.Origin,
		options.JobDefaults.RetryBackoff.Origin,
		options.JobDefaults.MemoTTL.Origin,
		options.Requires.Origin,
		options.Prefers.Origin,
		options.AllowRisk.Origin,
		options.SubmitEnvAllow.Origin,
		options.Resources.Origin,
		options.Resources.Value.Cores.Origin,
		options.Resources.Value.MemoryBytes.Origin,
		options.Pause.Origin,
		options.Pause.Value.Before.Origin,
		options.Pause.Value.After.Origin,
		options.Pause.Value.OnFailure.Origin,
		options.Checkout.Origin,
	}
	for _, origin := range options.Checkout.Value.Origins {
		origins = append(origins, origin)
	}
	return origins
}
