package settings

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

// safety: Compound settings merge only present fields; zero and empty collections are values.
func compound[T any](in Inputs, key string, get func(Values) *T, fallback T) (T, Origin, map[string]Origin) {
	var out T
	origin := Origin{Source: Default, Key: key}
	origins := map[string]Origin{}
	mergeFields(reflect.ValueOf(&out).Elem(), reflect.ValueOf(fallback), key, origin, origins)
	order := sources(in, key)
	for i := len(order) - 1; i >= 0; i-- {
		values := sourceValues(in, order[i])
		selected := get(values)
		if selected == nil {
			continue
		}
		from := originFor(values, order[i], key)
		layerOrigins := map[string]Origin{}
		mergeFields(reflect.ValueOf(&out).Elem(), reflect.ValueOf(selected).Elem(), key, from, layerOrigins)
		for path, leaf := range layerOrigins {
			if _, ok := values.Origins[path]; ok {
				leaf = originFor(values, order[i], path)
			}
			origins[path] = leaf
		}
		origin = from
	}
	return out, origin, origins
}

func mergeFields(dst, src reflect.Value, path string, origin Origin, origins map[string]Origin) {
	if src.Kind() == reflect.Struct {
		for i := 0; i < src.NumField(); i++ {
			name := strings.Split(src.Type().Field(i).Tag.Get("yaml"), ",")[0]
			if name == "" {
				name = strings.Split(src.Type().Field(i).Tag.Get("json"), ",")[0]
			}
			if name == "" {
				name = strings.ToLower(src.Type().Field(i).Name)
			}
			mergeFields(dst.Field(i), src.Field(i), path+"."+name, origin, origins)
		}
		return
	}
	if src.IsNil() {
		return
	}
	if src.Kind() == reflect.Pointer && src.Elem().Kind() == reflect.Struct {
		if dst.IsNil() {
			dst.Set(reflect.New(src.Type().Elem()))
		}
		mergeFields(dst.Elem(), src.Elem(), path, origin, origins)
		return
	}
	switch src.Kind() {
	case reflect.Pointer:
		value := reflect.New(src.Type().Elem())
		value.Elem().Set(src.Elem())
		dst.Set(value)
	case reflect.Map:
		value := reflect.MakeMapWithSize(src.Type(), src.Len()+dst.Len())
		if src.Len() > 0 && !dst.IsNil() {
			prior := dst.MapRange()
			for prior.Next() {
				value.SetMapIndex(prior.Key(), prior.Value())
			}
		}
		iter := src.MapRange()
		for iter.Next() {
			value.SetMapIndex(iter.Key(), iter.Value())
			origins[path+"."+iter.Key().String()] = origin
		}
		dst.Set(value)
	case reflect.Slice:
		value := reflect.MakeSlice(src.Type(), src.Len(), src.Len())
		reflect.Copy(value, src)
		dst.Set(value)
	}
	origins[path] = origin
}

func compoundError(err error, key string, origin Origin, origins map[string]Origin) error {
	if err == nil {
		return nil
	}
	var field *userconfig.FieldError
	if errors.As(err, &field) {
		path := field.Key
		if key == "machine.runner" {
			path = "machine." + path
		}
		for candidate := path; candidate != ""; {
			if from, ok := origins[candidate]; ok {
				origin = from
				break
			}
			i := strings.LastIndexByte(candidate, '.')
			if i < 0 {
				break
			}
			candidate = candidate[:i]
		}
		return invalid(path, origin, field.Problem)
	}
	return invalid(key, origin, "invalid configuration")
}

// RunnerPreferences validates the merged renewal schedule before any runner starts.
func RunnerPreferences(in Inputs) (userconfig.RunnerConfig, Origin, error) {
	defaults := userconfig.RunnerConfig{MaxConcurrent: pointer(1), Poll: pointer(limits.ClaimPoll.String()), Lease: pointer(limits.DefaultLease.String())}
	value, origin, origins := compound(in, "machine.runner", func(v Values) *userconfig.RunnerConfig { return v.RunnerPreferences }, defaults)
	if value.Heartbeat == nil {
		lease, err := time.ParseDuration(*value.Lease)
		if err != nil {
			return value, origin, invalid("machine.runner.lease", origins["machine.runner.lease"], "invalid lease duration")
		}
		value.Heartbeat = pointer(limits.Renew(lease).String())
	}
	return value, origin, compoundError(userconfig.ValidateConfig(userconfig.Config{Runner: &value}), "machine.runner", origin, origins)
}

// RepoLocation consumes ordered checkout facts, never ambient working directories.
func RepoLocation(in Inputs) (string, Origin, error) {
	fallback := in.Context.ParentCheckout
	if fallback == "" && len(in.Context.RegisteredCheckouts) > 0 {
		fallback = in.Context.RegisteredCheckouts[0]
	}
	if fallback == "" && len(in.Context.FallbackPaths) > 0 {
		fallback = in.Context.FallbackPaths[0]
	}
	value, origin, err := pickRun(in, "repo_location", func(v runspec.Options) runspec.Setting[string] { return v.RepoLocation }, fallback)
	if err != nil {
		return value, origin, err
	}
	if in.Context.Submitter && in.Context.ParentCheckout != "" && !in.options(Flag).RepoLocation.Set && !in.options(Run).RepoLocation.Set {
		value = in.Context.ParentCheckout
		origin = Origin{Source: Run, Key: "repo_location"}
	}
	if strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) {
		return "", origin, invalid("repo_location", origin, "checkout path is absent or invalid")
	}
	return filepath.Clean(value), origin, nil
}

func pauseSetting(in Inputs) (runspec.Setting[runspec.PauseSelection], error) {
	if !in.Context.Submitter && !in.options(Run).Pause.Set {
		return runspec.Setting[runspec.PauseSelection]{}, invalid("debug.pause", Origin{Source: Run, Key: "debug.pause"}, "sealed setting is absent")
	}
	origin := runspec.SettingOrigin{Layer: runspec.LayerDefault, Key: "debug.pause"}
	out := runspec.PauseSelection{Before: declaration([]string{}, runspec.SettingOrigin{Layer: runspec.LayerDefault, Key: "debug.pause.before"}), After: declaration([]string{}, runspec.SettingOrigin{Layer: runspec.LayerDefault, Key: "debug.pause.after"}), OnFailure: declaration(false, runspec.SettingOrigin{Layer: runspec.LayerDefault, Key: "debug.pause.on_failure"})}
	order := sources(in, "debug.pause")
	for i := len(order) - 1; i >= 0; i-- {
		setting := in.options(order[i]).Pause
		if !setting.Set {
			continue
		}
		origin = runOrigin(setting.Origin, order[i], "debug.pause")
		if setting.Value.Before.Set {
			out.Before = setting.Value.Before
			out.Before.Origin = nestedRunOrigin(out.Before.Origin, setting.Origin, order[i], "debug.pause.before")
			out.Before.Value = slices.Clone(out.Before.Value)
		}
		if setting.Value.After.Set {
			out.After = setting.Value.After
			out.After.Origin = nestedRunOrigin(out.After.Origin, setting.Origin, order[i], "debug.pause.after")
			out.After.Value = slices.Clone(out.After.Value)
		}
		if setting.Value.OnFailure.Set {
			out.OnFailure = setting.Value.OnFailure
			out.OnFailure.Origin = nestedRunOrigin(out.OnFailure.Origin, setting.Origin, order[i], "debug.pause.on_failure")
		}
	}
	for _, part := range []struct {
		key     string
		setting runspec.Setting[[]string]
	}{{"before", out.Before}, {"after", out.After}} {
		from := fromOrigin(part.setting.Origin)
		if from.Source == "" {
			return runspec.Setting[runspec.PauseSelection]{}, invalid("debug.pause."+part.key, from, "unknown setting origin")
		}
		if err := validNames("debug.pause."+part.key, part.setting.Value, from); err != nil {
			return runspec.Setting[runspec.PauseSelection]{}, err
		}
	}
	return declaration(out, origin), nil
}
