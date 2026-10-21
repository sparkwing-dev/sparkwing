package settings

import (
	"maps"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func (in Inputs) options(source Source) runspec.Options { return sourceValues(in, source).Options }

func runOrigin(origin runspec.SettingOrigin, source Source, key string) runspec.SettingOrigin {
	if source != Run || origin.Layer == "" {
		origin.Layer = OriginLayers[source]
	}
	if origin.Key == "" {
		origin.Key = key
	}
	return origin
}

func nestedRunOrigin(origin, parent runspec.SettingOrigin, source Source, key string) runspec.SettingOrigin {
	if origin == (runspec.SettingOrigin{}) {
		origin = parent
		origin.Key = key
	}
	return runOrigin(origin, source, key)
}

func pickRun[T any](in Inputs, key string, field func(runspec.Options) runspec.Setting[T], fallback T) (T, Origin, error) {
	for _, source := range sources(in, key) {
		setting := field(in.options(source))
		if setting.Set {
			origin := fromOrigin(runOrigin(setting.Origin, source, key))
			if origin.Source == "" {
				return setting.Value, origin, invalid(key, origin, "unknown setting origin")
			}
			return setting.Value, origin, nil
		}
	}
	if !in.Context.Submitter {
		return fallback, Origin{Source: Run, Key: key}, invalid(key, Origin{Source: Run, Key: key}, "sealed setting is absent")
	}
	return fallback, Origin{Source: Default, Key: key}, nil
}

func pickRunList(in Inputs, key string, field func(runspec.Options) runspec.Setting[[]string]) ([]string, Origin, error) {
	value, origin, err := pickRun(in, key, field, []string{})
	return slices.Clone(value), origin, err
}

func cloneOptions(out runspec.Options) runspec.Options {
	out.Requires.Value = slices.Clone(out.Requires.Value)
	out.Prefers.Value = slices.Clone(out.Prefers.Value)
	out.AllowRisk.Value = slices.Clone(out.AllowRisk.Value)
	out.SubmitEnvAllow.Value = slices.Clone(out.SubmitEnvAllow.Value)
	out.Pause.Value.Before.Value = slices.Clone(out.Pause.Value.Before.Value)
	out.Pause.Value.After.Value = slices.Clone(out.Pause.Value.After.Value)
	out.Checkout.Value.Policy = cloneSource(out.Checkout.Value.Policy)
	out.Checkout.Value.Origins = maps.Clone(out.Checkout.Value.Origins)
	out.JobDefaults.Requires.Value = slices.Clone(out.JobDefaults.Requires.Value)
	out.JobDefaults.Prefers.Value = slices.Clone(out.JobDefaults.Prefers.Value)
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
