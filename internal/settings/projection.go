package settings

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// ProjectOrigin retains file, key and line and rejects unknown source values.
// A sealed winner keeps its original origin; Run maps to LayerSealed only for raw run input.
func ProjectOrigin(origin Origin) (runspec.SettingOrigin, error) {
	layer, ok := OriginLayers[origin.Source]
	if !ok {
		return runspec.SettingOrigin{}, fmt.Errorf("unknown setting source %q", origin.Source)
	}
	return runspec.SettingOrigin{Layer: layer, File: origin.File, Key: origin.Key, Line: origin.Line}, nil
}

func fromOrigin(origin runspec.SettingOrigin) Origin {
	for source, layer := range OriginLayers {
		if layer == origin.Layer {
			return Origin{Source: source, File: origin.File, Key: origin.Key, Line: origin.Line}
		}
	}
	return Origin{File: origin.File, Key: origin.Key, Line: origin.Line}
}

func projectValue[T any](value *T, origins map[string]Origin, key string) (runspec.Setting[T], error) {
	if value == nil {
		return runspec.Setting[T]{}, nil
	}
	origin, ok := origins[key]
	if !ok {
		return runspec.Setting[T]{}, invalid(key, Origin{}, "present setting has no origin")
	}
	projected, err := ProjectOrigin(origin)
	if err != nil {
		return runspec.Setting[T]{}, err
	}
	return runspec.Setting[T]{Optional: runspec.Optional[T]{Set: true, Value: *value}, Origin: projected}, nil
}

func projectList(value []string, origins map[string]Origin, key string) (runspec.Setting[[]string], error) {
	if value == nil {
		return runspec.Setting[[]string]{}, nil
	}
	return projectValue(pointer(slices.Clone(value)), origins, key)
}

func nestedOrigins(origins map[string]Origin, parent string, children ...string) map[string]Origin {
	out := maps.Clone(origins)
	if out == nil {
		out = map[string]Origin{}
	}
	for _, child := range children {
		key := parent + "." + child
		if _, ok := out[key]; !ok {
			if origin, ok := out[parent]; ok {
				origin.Key = key
				out[key] = origin
			}
		}
	}
	return out
}

func projectResources(value *Resources, origins map[string]Origin) (runspec.Setting[runspec.Resources], error) {
	if value == nil {
		return runspec.Setting[runspec.Resources]{}, nil
	}
	origins = nestedOrigins(origins, "resources", "cores", "memory_bytes")
	cores, err := projectValue(value.Cores, origins, "resources.cores")
	if err != nil {
		return runspec.Setting[runspec.Resources]{}, err
	}
	memory, err := projectValue(value.MemoryBytes, origins, "resources.memory_bytes")
	if err != nil {
		return runspec.Setting[runspec.Resources]{}, err
	}
	return projectValue(&runspec.Resources{Cores: cores, MemoryBytes: memory}, origins, "resources")
}

func projectPause(value *PauseSelection, origins map[string]Origin) (runspec.Setting[runspec.PauseSelection], error) {
	if value == nil {
		return runspec.Setting[runspec.PauseSelection]{}, nil
	}
	origins = nestedOrigins(origins, "debug.pause", "before", "after", "on_failure")
	before, err := projectList(value.Before, origins, "debug.pause.before")
	if err != nil {
		return runspec.Setting[runspec.PauseSelection]{}, err
	}
	after, err := projectList(value.After, origins, "debug.pause.after")
	if err != nil {
		return runspec.Setting[runspec.PauseSelection]{}, err
	}
	failure, err := projectValue(value.OnFailure, origins, "debug.pause.on_failure")
	if err != nil {
		return runspec.Setting[runspec.PauseSelection]{}, err
	}
	return projectValue(&runspec.PauseSelection{Before: before, After: after, OnFailure: failure}, origins, "debug.pause")
}

func projectCheckout(value *nodeproto.SourceSpec, origins map[string]Origin) (runspec.Setting[runspec.CheckoutPolicy], error) {
	if value == nil {
		return runspec.Setting[runspec.CheckoutPolicy]{}, nil
	}
	out := runspec.CheckoutPolicy{Policy: cloneSource(*value), Origins: map[string]runspec.SettingOrigin{}}
	origins = nestedOrigins(origins, "source", "full_history", "depth", "tags", "submodules", "lfs")
	fields := map[string]bool{"full_history": value.FullHistory != nil, "depth": value.Depth != nil, "tags": value.Tags != nil, "submodules": value.Submodules != nil, "lfs": value.LFS != nil}
	for field, present := range fields {
		if present {
			origin, err := ProjectOrigin(origins["source."+field])
			if err != nil {
				return runspec.Setting[runspec.CheckoutPolicy]{}, err
			}
			out.Origins[field] = origin
		}
	}
	return projectValue(&out, origins, "source")
}

// ProjectRequest preserves presence and original origins when values enter C1.
// It maps only request-owned fields; startup credentials and machine runner config stay local.
// Pointers, nil collections and Origins preserve unset/empty/zero. Nested keys carry their own origins.
// RequestProjection lists every run-owned Values field and its C1 destination.
func ProjectRequest(values Values) (runspec.Options, error) {
	out := runspec.Options{}
	var err error
	out.Runner, err = projectValue(values.RunnerSelection, values.Origins, "runner")
	if err != nil {
		return runspec.Options{}, err
	}
	out.Resources, err = projectResources(values.Resources, values.Origins)
	if err != nil {
		return runspec.Options{}, err
	}
	out.Priority, err = projectValue(values.Priority, values.Origins, "priority")
	if err != nil {
		return runspec.Options{}, err
	}
	out.AdmissionClass, err = projectValue(values.AdmissionClass, values.Origins, "admission_class")
	if err != nil {
		return runspec.Options{}, err
	}
	out.ClaimWait, err = projectValue(values.ClaimWait, values.Origins, "claim_wait")
	if err != nil {
		return runspec.Options{}, err
	}
	out.OwnerExecutionTimeout, err = projectValue(values.OwnerExecution, values.Origins, "owner_execution")
	if err != nil {
		return runspec.Options{}, err
	}
	out.Checkout, err = projectCheckout(values.Checkout, values.Origins)
	if err != nil {
		return runspec.Options{}, err
	}
	out.Requires, err = projectList(values.Requires, values.Origins, "requires")
	if err != nil {
		return runspec.Options{}, err
	}
	out.Prefers, err = projectList(values.Prefers, values.Origins, "prefers")
	if err != nil {
		return runspec.Options{}, err
	}
	out.StartAt, err = projectValue(values.StartAt, values.Origins, "start_at")
	if err != nil {
		return runspec.Options{}, err
	}
	out.StopAt, err = projectValue(values.StopAt, values.Origins, "stop_at")
	if err != nil {
		return runspec.Options{}, err
	}
	out.Only, err = projectValue(values.Only, values.Origins, "only")
	if err != nil {
		return runspec.Options{}, err
	}
	out.DryRun, err = projectValue(values.DryRun, values.Origins, "dry_run")
	if err != nil {
		return runspec.Options{}, err
	}
	out.NoMemo, err = projectValue(values.NoMemo, values.Origins, "no_memo")
	if err != nil {
		return runspec.Options{}, err
	}
	out.AllowRisk, err = projectList(values.AllowRisk, values.Origins, "allow_risk")
	if err != nil {
		return runspec.Options{}, err
	}
	out.Workers, err = projectValue(values.Workers, values.Origins, "workers")
	if err != nil {
		return runspec.Options{}, err
	}
	out.Output, err = projectValue(values.Output, values.Origins, "output")
	if err != nil {
		return runspec.Options{}, err
	}
	out.Pause, err = projectPause(values.Pause, values.Origins)
	if err != nil {
		return runspec.Options{}, err
	}
	out.PauseTimeout, err = projectValue(values.PauseTimeout, values.Origins, "debug.pause_timeout")
	if err != nil {
		return runspec.Options{}, err
	}
	out.Debug, err = projectValue(values.Debug, values.Origins, "debug")
	if err != nil {
		return runspec.Options{}, err
	}
	out.Publish, err = projectValue(values.Publish, values.Origins, "publish")
	if err != nil {
		return runspec.Options{}, err
	}
	out.CacheFrom, err = projectValue(values.CacheFrom, values.Origins, "cache_from")
	if err != nil {
		return runspec.Options{}, err
	}
	out.SubmitEnvAllow, err = projectList(values.SubmitEnvAllow, values.Origins, "run.submit_env_allow")
	if err != nil {
		return runspec.Options{}, err
	}
	out.RepoLocation, err = projectValue(values.RepoLocation, values.Origins, "repo_location")
	if err != nil {
		return runspec.Options{}, err
	}
	out.Profile, err = projectValue(values.Profile, values.Origins, "profile")
	if err != nil {
		return runspec.Options{}, err
	}
	out.JobDefaults.Timeout, err = projectValue(values.JobTimeout, values.Origins, "job.timeout")
	if err != nil {
		return runspec.Options{}, err
	}
	out.JobDefaults.NoProgressTimeout, err = projectValue(values.NoProgressTimeout, values.Origins, "job.no_progress_timeout")
	if err != nil {
		return runspec.Options{}, err
	}
	out.JobDefaults.ApprovalWait, err = projectValue(values.ApprovalWait, values.Origins, "approval.wait")
	if err != nil {
		return runspec.Options{}, err
	}
	out.JobDefaults.Retry, err = projectValue(values.Retry, values.Origins, "job.retry")
	if err != nil {
		return runspec.Options{}, err
	}
	out.JobDefaults.RetryBackoff, err = projectValue(values.RetryBackoff, values.Origins, "job.retry_backoff")
	if err != nil {
		return runspec.Options{}, err
	}
	out.JobDefaults.MemoTTL, err = projectValue(values.MemoTTL, values.Origins, "job.memo_ttl")
	if err != nil {
		return runspec.Options{}, err
	}
	return out, nil
}

// ProjectCeilings preserves operator origins and adds C4's fixed retry/memo bounds.
// AllowedSources is a set of complete checkout policies; unset permits all, empty permits none.
// Every tuple field must be present, including explicit false. Matching compares all fields.
func ProjectCeilings(values OperatorValues) (runspec.Ceilings, error) {
	out := runspec.Ceilings{}
	var err error
	out.OwnerExecutionTimeout, err = projectValue(values.OwnerExecution, values.Origins, "operator.owner_execution")
	if err != nil {
		return out, err
	}
	out.Priority, err = projectValue(values.MaxPriority, values.Origins, "operator.max_priority")
	if err != nil {
		return out, err
	}
	out.ClaimWait, err = projectValue(values.MaxClaimWait, values.Origins, "operator.max_claim_wait")
	if err != nil {
		return out, err
	}
	if values.MaxCores != nil || values.MaxMemoryBytes != nil {
		origins := map[string]Origin{"resources": values.Origins["operator.max_cores"], "resources.cores": values.Origins["operator.max_cores"], "resources.memory_bytes": values.Origins["operator.max_memory_bytes"]}
		if values.MaxCores == nil {
			origins["resources"] = origins["resources.memory_bytes"]
		}
		out.Resources, err = projectResources(&Resources{Cores: values.MaxCores, MemoryBytes: values.MaxMemoryBytes}, origins)
		if err != nil {
			return out, err
		}
	}
	out.AllowedAdmissionClasses, err = projectList(values.AllowedAdmissionClasses, values.Origins, "operator.allowed_admission_classes")
	if err != nil {
		return out, err
	}
	if values.AllowedSources != nil {
		policies := make([]nodeproto.SourceSpec, len(values.AllowedSources))
		for i, policy := range values.AllowedSources {
			if err := validateSource(policy, true); err != nil {
				return out, err
			}
			policies[i] = cloneSource(policy)
		}
		out.AllowedSources, err = projectValue(&policies, values.Origins, "operator.allowed_sources")
		if err != nil {
			return out, err
		}
	}
	out.Retry = runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: limits.MaxRetries}, Origin: runspec.SettingOrigin{Layer: runspec.LayerDefault, Key: "job.retry"}}
	out.MemoTTL = runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: limits.MemoMaxTTL}, Origin: runspec.SettingOrigin{Layer: runspec.LayerDefault, Key: "job.memo_ttl"}}
	return out, nil
}
