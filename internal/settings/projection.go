package settings

import (
	"fmt"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// projectOrigin retains file, key and line and rejects unknown source values.
// A sealed winner keeps its original origin; Run maps to LayerSealed only for raw run input.
func projectOrigin(origin Origin) (runspec.SettingOrigin, error) {
	layer, ok := originLayers[origin.Source]
	if !ok {
		return runspec.SettingOrigin{}, fmt.Errorf("unknown setting source %q", origin.Source)
	}
	return runspec.SettingOrigin{Layer: layer, File: origin.File, Key: origin.Key, Line: origin.Line}, nil
}

func fromOrigin(origin runspec.SettingOrigin) Origin {
	for source, layer := range originLayers {
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
	projected, err := projectOrigin(origin)
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
