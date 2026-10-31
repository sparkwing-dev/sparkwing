package settings

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func resolveSetting[T any](in Inputs, resolver func(Inputs) (T, Origin, error), field *runspec.Setting[T]) error {
	value, origin, err := resolver(in)
	if err != nil {
		return err
	}
	projected, err := projectOrigin(origin)
	if err != nil {
		return err
	}
	*field = declaration(value, projected)
	return nil
}

// ResolveIntake resolves captured request preferences against trusted controller inputs.
func ResolveIntake(options runspec.Options, operator OperatorInputs, ctx Context) (runspec.Options, error) {
	for _, origin := range intakeOrigins(&options) {
		switch origin.Layer {
		case "", runspec.LayerFlag, runspec.LayerEnv, runspec.LayerSealed, runspec.LayerMachine, runspec.LayerDefault:
		default:
			return runspec.Options{}, invalid(origin.Key, Origin{Source: sourceRun, Key: origin.Key}, "request setting has an invalid intake origin")
		}
	}
	return ResolveRequest(Inputs{Sealed: Values{Options: options}, Operator: operator, Context: ctx})
}

// InheritedOptions captures a parent's winners as child intake inputs. Their
// original policy layers cannot be asserted by a new request; intake reapplies
// the controller's ceilings and parent-only owner checks.
func InheritedOptions(options runspec.Options) runspec.Options {
	options = cloneOptions(options)
	for _, origin := range intakeOrigins(&options) {
		origin.Layer = runspec.LayerSealed
	}
	for key, origin := range options.Checkout.Value.Origins {
		origin.Layer = runspec.LayerSealed
		options.Checkout.Value.Origins[key] = origin
	}
	return options
}

// ResolveRequest resolves only intake inputs; project and code fields remain
// unset and their winning request overrides remain distinguishable from defaults.
// The store owns sealing and write-once enforcement in runspec.Request.
func ResolveRequest(in Inputs) (runspec.Options, error) {
	in.Context.Submitter = true
	in.Pipeline = Values{}
	in.Defaults = Values{}
	in.Code = Values{}
	flags, sealed, machine := in.Flags.Options, in.Sealed.Options, in.Machine.Options
	out := runspec.Options{}
	var err error
	if err = resolveSetting(in, runnerSelection, &out.Runner); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, OwnerExecution, &out.OwnerExecutionTimeout); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, workers, &out.Workers); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, output, &out.Output); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, startAt, &out.StartAt); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, stopAt, &out.StopAt); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, only, &out.Only); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, dryRun, &out.DryRun); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, noMemo, &out.NoMemo); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, allowRisk, &out.AllowRisk); err != nil {
		return runspec.Options{}, err
	}
	out.Pause, err = pauseSetting(in)
	if err != nil {
		return out, err
	}
	if err = resolveSetting(in, pauseTimeout, &out.PauseTimeout); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, debug, &out.Debug); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, publish, &out.Publish); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, cacheFrom, &out.CacheFrom); err != nil {
		return runspec.Options{}, err
	}
	if err = resolveSetting(in, submitEnvAllow, &out.SubmitEnvAllow); err != nil {
		return runspec.Options{}, err
	}
	if flags.RepoLocation.Set || sealed.RepoLocation.Set || machine.RepoLocation.Set || in.Context.ParentCheckout != "" || len(in.Context.RegisteredCheckouts) > 0 || len(in.Context.FallbackPaths) > 0 {
		if err = resolveSetting(in, repoLocation, &out.RepoLocation); err != nil {
			return runspec.Options{}, err
		}
	}
	if flags.Priority.Set || sealed.Priority.Set {
		if err = resolveSetting(in, priority, &out.Priority); err != nil {
			return runspec.Options{}, err
		}
	}
	if flags.Profile.Set || sealed.Profile.Set {
		if err = resolveSetting(in, profile, &out.Profile); err != nil {
			return runspec.Options{}, err
		}
	}
	if flags.Requires.Set || sealed.Requires.Set {
		if err = resolveSetting(in, requires, &out.Requires); err != nil {
			return runspec.Options{}, err
		}
	}
	if flags.Prefers.Set || sealed.Prefers.Set {
		if err = resolveSetting(in, prefers, &out.Prefers); err != nil {
			return runspec.Options{}, err
		}
	}
	if sealed.AdmissionClass.Set {
		if err = resolveSetting(in, admissionClass, &out.AdmissionClass); err != nil {
			return runspec.Options{}, err
		}
	}
	if sealed.ClaimWait.Set {
		if err = resolveSetting(in, ClaimWait, &out.ClaimWait); err != nil {
			return runspec.Options{}, err
		}
	}
	if sealed.JobDefaults.Retry.Set {
		if err = resolveSetting(in, Retry, &out.JobDefaults.Retry); err != nil {
			return runspec.Options{}, err
		}
	}
	if sealed.JobDefaults.Timeout.Set {
		if err = resolveSetting(in, JobTimeout, &out.JobDefaults.Timeout); err != nil {
			return runspec.Options{}, err
		}
	}
	if sealed.JobDefaults.NoProgressTimeout.Set {
		if err = resolveSetting(in, noProgressTimeout, &out.JobDefaults.NoProgressTimeout); err != nil {
			return runspec.Options{}, err
		}
	}
	if sealed.JobDefaults.ApprovalWait.Set {
		if err = resolveSetting(in, ApprovalWait, &out.JobDefaults.ApprovalWait); err != nil {
			return runspec.Options{}, err
		}
	}
	if sealed.JobDefaults.RetryBackoff.Set {
		if err = resolveSetting(in, retryBackoff, &out.JobDefaults.RetryBackoff); err != nil {
			return runspec.Options{}, err
		}
	}
	if sealed.JobDefaults.MemoTTL.Set {
		if err = resolveSetting(in, MemoTTL, &out.JobDefaults.MemoTTL); err != nil {
			return runspec.Options{}, err
		}
	}
	if sealed.Checkout.Set {
		if err := validateSource(sealed.Checkout.Value.Policy, false); err != nil {
			return out, invalid("source", fromOrigin(sealed.Checkout.Origin), err.Error())
		}
		out.Checkout = cloneOptions(sealed).Checkout
	}

	return out, nil
}

func milliseconds(value *int64, key string) (*time.Duration, error) {
	if value == nil {
		return nil, nil
	}
	if *value < 0 || *value > int64((1<<63-1)/time.Millisecond) {
		return nil, invalid(key, Origin{Source: code, Key: key}, "duration is negative or overflows")
	}
	return pointer(time.Duration(*value) * time.Millisecond), nil
}
