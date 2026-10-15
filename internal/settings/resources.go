package settings

import (
	"fmt"
	"math"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func dimension[T any](in Inputs, key string, field func(Values) *T) (*T, Origin) {
	for _, source := range sources(in, "resources") {
		values := sourceValues(in, source)
		if value := field(values); value != nil {
			origin := originFor(values, source, key)
			if _, ok := values.Origins[key]; !ok {
				origin = originFor(values, source, "resources")
				origin.Key = key
			}
			return pointer(*value), origin
		}
	}
	return nil, Origin{Source: Default, Key: key}
}

func resourceLimits(in Inputs) (Resources, map[string]Origin, Origin, error) {
	cores, coreOrigin := dimension(in, "resources.cores", func(v Values) *float64 {
		if v.Resources == nil {
			return nil
		}
		return v.Resources.Cores
	})
	memory, memoryOrigin := dimension(in, "resources.memory_bytes", func(v Values) *int64 {
		if v.Resources == nil {
			return nil
		}
		return v.Resources.MemoryBytes
	})
	out := Resources{Cores: cores, MemoryBytes: memory}
	origins := map[string]Origin{"resources.cores": coreOrigin, "resources.memory_bytes": memoryOrigin}
	origin := coreOrigin
	if cores == nil {
		origin = memoryOrigin
	}
	op := OperatorValues{}
	if in.Context.Submitter {
		var err error
		op, err = ControllerCeilings(in.Operator)
		if err != nil {
			return out, origins, origin, err
		}
	}
	if cores != nil {
		if *cores <= 0 || math.IsNaN(*cores) || math.IsInf(*cores, 0) {
			return out, origins, origin, invalid("resources.cores", coreOrigin, "must be positive and finite")
		}
		if op.MaxCores != nil && *cores > *op.MaxCores {
			return out, origins, origin, above("resources.cores", coreOrigin, *cores, *op.MaxCores, op.Origins["operator.max_cores"])
		}
	}
	if memory != nil {
		if *memory <= 0 {
			return out, origins, origin, invalid("resources.memory_bytes", memoryOrigin, "must be positive")
		}
		if op.MaxMemoryBytes != nil && *memory > *op.MaxMemoryBytes {
			return out, origins, origin, above("resources.memory_bytes", memoryOrigin, *memory, *op.MaxMemoryBytes, op.Origins["operator.max_memory_bytes"])
		}
	}
	return out, origins, origin, nil
}

// ResourceLimits merges native dimensions and refuses each excess independently.
func ResourceLimits(in Inputs) (Resources, Origin, error) {
	value, _, origin, err := resourceLimits(in)
	return value, origin, err
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	return pointer(*value)
}

func cloneSource(source nodeproto.SourceSpec) nodeproto.SourceSpec {
	return nodeproto.SourceSpec{FullHistory: clonePointer(source.FullHistory), Depth: clonePointer(source.Depth), Tags: clonePointer(source.Tags), Submodules: clonePointer(source.Submodules), LFS: clonePointer(source.LFS)}
}

func validateSource(source nodeproto.SourceSpec, complete bool) error {
	if complete && (source.FullHistory == nil || source.Depth == nil || source.Tags == nil || source.Submodules == nil || source.LFS == nil) {
		return fmt.Errorf("source policy must specify all fields")
	}
	if source.Depth != nil && *source.Depth < 0 {
		return fmt.Errorf("source depth must be nonnegative")
	}
	if source.FullHistory != nil && *source.FullHistory && source.Depth != nil && *source.Depth != 0 {
		return fmt.Errorf("full_history conflicts with nonzero depth")
	}
	return nil
}

func sourceField[T any](in Inputs, key string, field func(nodeproto.SourceSpec) *T, fallback T) (*T, Origin) {
	for _, source := range sources(in, "source") {
		values := sourceValues(in, source)
		if values.Checkout == nil {
			continue
		}
		if value := field(*values.Checkout); value != nil {
			origin := originFor(values, source, "source."+key)
			if _, ok := values.Origins["source."+key]; !ok {
				origin = originFor(values, source, "source")
				origin.Key = "source." + key
			}
			return pointer(*value), origin
		}
	}
	return pointer(fallback), Origin{Source: Default, Key: "source." + key}
}

func checkout(in Inputs) (nodeproto.SourceSpec, map[string]Origin, Origin, error) {
	if !in.Context.Submitter {
		if in.Sealed.Checkout == nil || validateSource(*in.Sealed.Checkout, true) != nil {
			return nodeproto.SourceSpec{}, nil, Origin{Source: Run, Key: "source"}, invalid("source", Origin{Source: Run, Key: "source"}, "sealed checkout policy is absent or incomplete")
		}
	}
	out := nodeproto.SourceSpec{}
	origins := map[string]Origin{}
	out.FullHistory, origins["source.full_history"] = sourceField(in, "full_history", func(v nodeproto.SourceSpec) *bool { return v.FullHistory }, false)
	out.Depth, origins["source.depth"] = sourceField(in, "depth", func(v nodeproto.SourceSpec) *int { return v.Depth }, 1)
	out.Tags, origins["source.tags"] = sourceField(in, "tags", func(v nodeproto.SourceSpec) *bool { return v.Tags }, false)
	out.Submodules, origins["source.submodules"] = sourceField(in, "submodules", func(v nodeproto.SourceSpec) *bool { return v.Submodules }, false)
	out.LFS, origins["source.lfs"] = sourceField(in, "lfs", func(v nodeproto.SourceSpec) *bool { return v.LFS }, false)
	origin := Origin{Source: Default, Key: "source"}
	for _, source := range sources(in, "source") {
		if v := sourceValues(in, source); v.Checkout != nil {
			origin = originFor(v, source, "source")
			break
		}
	}
	if *out.FullHistory && origins["source.depth"].Source == Default {
		out.Depth = pointer(0)
		origins["source.depth"] = origins["source.full_history"]
	}
	if *out.Depth == 0 && origins["source.full_history"].Source == Default {
		out.FullHistory = pointer(true)
		origins["source.full_history"] = origins["source.depth"]
	}
	if err := validateSource(out, true); err != nil {
		return out, origins, origin, invalid("source", origin, err.Error())
	}
	op := OperatorValues{}
	if in.Context.Submitter {
		var err error
		op, err = ControllerCeilings(in.Operator)
		if err != nil {
			return out, origins, origin, err
		}
	}
	if op.AllowedSources != nil {
		allowed := false
		for _, policy := range op.AllowedSources {
			if reflect.DeepEqual(out, policy) {
				allowed = true
				break
			}
		}
		if !allowed {
			return out, origins, origin, invalid("source", origin, "checkout policy is not permitted by operator")
		}
	}
	return out, origins, origin, nil
}
