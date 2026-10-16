package settings

import (
	"fmt"
	"math"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func dimension[T any](in Inputs, key string, field func(runspec.Resources) runspec.Setting[T]) runspec.Setting[T] {
	for _, source := range sources(in, "resources") {
		resources := in.options(source).Resources
		if value := field(resources.Value); resources.Set && value.Set {
			value.Origin = nestedRunOrigin(value.Origin, resources.Origin, source, key)
			return value
		}
	}
	return runspec.Setting[T]{}
}

func resourceSetting(in Inputs) (runspec.Setting[runspec.Resources], error) {
	cores := dimension(in, "resources.cores", func(v runspec.Resources) runspec.Setting[float64] { return v.Cores })
	memory := dimension(in, "resources.memory_bytes", func(v runspec.Resources) runspec.Setting[int64] { return v.MemoryBytes })
	if !cores.Set && !memory.Set {
		return runspec.Setting[runspec.Resources]{}, nil
	}
	origin := cores.Origin
	if !cores.Set {
		origin = memory.Origin
	}
	out := declaration(runspec.Resources{Cores: cores, MemoryBytes: memory}, origin)
	op := runspec.Ceilings{}
	if in.Context.Submitter {
		var err error
		op, err = operatorCeilings(in)
		if err != nil {
			return out, err
		}
	}
	if cores.Set {
		from := fromOrigin(cores.Origin)
		if from.Source == "" || cores.Value <= 0 || math.IsNaN(cores.Value) || math.IsInf(cores.Value, 0) {
			return out, invalid("resources.cores", from, "must be positive and finite with a known origin")
		}
		if ceiling := op.Resources.Value.Cores; op.Resources.Set && ceiling.Set && cores.Value > ceiling.Value {
			return out, above("resources.cores", from, cores.Value, ceiling.Value, fromOrigin(ceiling.Origin))
		}
	}
	if memory.Set {
		from := fromOrigin(memory.Origin)
		if from.Source == "" || memory.Value <= 0 {
			return out, invalid("resources.memory_bytes", from, "must be positive with a known origin")
		}
		if ceiling := op.Resources.Value.MemoryBytes; op.Resources.Set && ceiling.Set && memory.Value > ceiling.Value {
			return out, above("resources.memory_bytes", from, memory.Value, ceiling.Value, fromOrigin(ceiling.Origin))
		}
	}
	return out, nil
}

// ResourceLimits merges native dimensions and refuses each excess independently.
func ResourceLimits(in Inputs) (runspec.Resources, Origin, error) {
	setting, err := resourceSetting(in)
	origin := fromOrigin(setting.Origin)
	if !setting.Set {
		origin = Origin{Source: Default, Key: "resources.cores"}
	}
	return setting.Value, origin, err
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

func sourceField[T any](in Inputs, key string, field func(nodeproto.SourceSpec) *T, fallback T) (*T, runspec.SettingOrigin) {
	for _, source := range sources(in, "source") {
		setting := in.options(source).Checkout
		if value := field(setting.Value.Policy); setting.Set && value != nil {
			origin, ok := setting.Value.Origins[key]
			if !ok {
				origin = setting.Origin
				origin.Key = "source." + key
			}
			return pointer(*value), runOrigin(origin, source, "source."+key)
		}
	}
	return pointer(fallback), runspec.SettingOrigin{Layer: runspec.LayerDefault, Key: "source." + key}
}

func checkoutSetting(in Inputs) (runspec.Setting[runspec.CheckoutPolicy], error) {
	if !in.Context.Submitter {
		sealed := in.options(Run).Checkout
		if !sealed.Set || validateSource(sealed.Value.Policy, true) != nil {
			return runspec.Setting[runspec.CheckoutPolicy]{}, invalid("source", Origin{Source: Run, Key: "source"}, "sealed checkout policy is absent or incomplete")
		}
	}
	out := runspec.CheckoutPolicy{Origins: map[string]runspec.SettingOrigin{}}
	out.Policy.FullHistory, out.Origins["full_history"] = sourceField(in, "full_history", func(v nodeproto.SourceSpec) *bool { return v.FullHistory }, false)
	out.Policy.Depth, out.Origins["depth"] = sourceField(in, "depth", func(v nodeproto.SourceSpec) *int { return v.Depth }, 1)
	out.Policy.Tags, out.Origins["tags"] = sourceField(in, "tags", func(v nodeproto.SourceSpec) *bool { return v.Tags }, false)
	out.Policy.Submodules, out.Origins["submodules"] = sourceField(in, "submodules", func(v nodeproto.SourceSpec) *bool { return v.Submodules }, false)
	out.Policy.LFS, out.Origins["lfs"] = sourceField(in, "lfs", func(v nodeproto.SourceSpec) *bool { return v.LFS }, false)
	origin := runspec.SettingOrigin{Layer: runspec.LayerDefault, Key: "source"}
	for _, source := range sources(in, "source") {
		if setting := in.options(source).Checkout; setting.Set {
			origin = runOrigin(setting.Origin, source, "source")
			break
		}
	}
	if *out.Policy.FullHistory && out.Origins["depth"].Layer == runspec.LayerDefault {
		out.Policy.Depth = pointer(0)
		out.Origins["depth"] = out.Origins["full_history"]
	}
	if *out.Policy.Depth == 0 && out.Origins["full_history"].Layer == runspec.LayerDefault {
		out.Policy.FullHistory = pointer(true)
		out.Origins["full_history"] = out.Origins["depth"]
	}
	result := declaration(out, origin)
	if err := validateSource(out.Policy, true); err != nil {
		return result, invalid("source", fromOrigin(origin), err.Error())
	}
	for _, fieldOrigin := range out.Origins {
		if fromOrigin(fieldOrigin).Source == "" {
			return result, invalid("source", fromOrigin(origin), "unknown setting origin")
		}
	}
	if in.Context.Submitter {
		op, err := operatorCeilings(in)
		if err != nil {
			return result, err
		}
		if op.AllowedSources.Set {
			for _, policy := range op.AllowedSources.Value {
				if reflect.DeepEqual(out.Policy, policy) {
					return result, nil
				}
			}
			return result, invalid("source", fromOrigin(origin), "checkout policy is not permitted by operator")
		}
	}
	return result, nil
}

func sourceSetting(source nodeproto.SourceSpec, origin Origin) runspec.Setting[runspec.CheckoutPolicy] {
	out := runspec.CheckoutPolicy{Policy: cloneSource(source), Origins: map[string]runspec.SettingOrigin{}}
	projected := runspec.SettingOrigin{Layer: OriginLayers[origin.Source], File: origin.File, Key: origin.Key, Line: origin.Line}
	fields := map[string]bool{"full_history": source.FullHistory != nil, "depth": source.Depth != nil, "tags": source.Tags != nil, "submodules": source.Submodules != nil, "lfs": source.LFS != nil}
	for field, present := range fields {
		if present {
			child := projected
			child.Key = "source." + field
			out.Origins[field] = child
		}
	}
	return declaration(out, projected)
}
