package sparkwing

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/swtags"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// ConfigField is one entry in the result of InspectPipelineConfig:
// the resolved value of one sw-tagged Config field plus which
// layering source contributed the winning value.
type ConfigField struct {
	// Name is the sw tag (e.g. "image_repo"). Matches keys in
	// pipelines.yaml values blocks.
	Name string
	// GoField is the Go struct field name (e.g. "ImageRepo").
	GoField string
	// TypeName is the Go type as printed by reflect.Type.String()
	// (e.g. "string", "int", "[]string").
	TypeName string
	// Value is the resolved value as the typed struct holds it.
	// Marshal-ready (json.Marshal renders it the same way the
	// pipeline body sees it).
	Value any
	// Source is a human-readable provenance string:
	//   - "pipelines.yaml targets.<target>.values"
	//   - "pipelines.yaml values.base"
	//   - "struct default"
	//   - "not set"
	Source string
	// Required reports whether the sw tag marked the field
	// `,required`. Reflects the declared contract, not the
	// resolved value.
	Required bool
}

// InspectPipelineConfig walks the same layering ResolvePipelineConfig
// uses, but records which source supplied the winning value per
// field instead of just returning the populated struct. Useful for
// operator-facing config introspection.
//
// Returns (nil, nil) when the pipeline does not implement
// ConfigProvider. Other failure modes (yaml type coercion, required
// missing) propagate as errors so the inspection surfaces the same
// failure the dispatcher would see.
func InspectPipelineConfig(reg *Registration, yamlEntry *pipelines.Pipeline, target, triggerSource string) ([]ConfigField, error) {
	if reg == nil || reg.instance == nil {
		return nil, nil
	}
	p := reg.instance()
	cp, ok := p.(ConfigProvider)
	if !ok {
		return nil, nil
	}
	cfg := cp.Config()
	if cfg == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(cfg)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("pipeline %q config: Config() must return a non-nil pointer to a struct, got %T", reg.Name, cfg)
	}
	specs, err := swtags.Parse(rv.Type())
	if err != nil {
		return nil, fmt.Errorf("pipeline %q config: %w", reg.Name, err)
	}
	elem := rv.Elem()
	sources := make(map[string]string, len(specs))

	// Layer 1: struct defaults.
	for _, s := range specs {
		if !s.HasDefault {
			continue
		}
		if err := swtags.CoerceAssign(elem.FieldByIndex(s.Field.Index), s.DefaultRaw, s.Field.Name); err != nil {
			return nil, fmt.Errorf("pipeline %q config: %w", reg.Name, err)
		}
		sources[s.Name] = "struct default"
	}

	// Layer 2: pipelines.yaml values.base.
	if yamlEntry != nil && len(yamlEntry.Values.Base) > 0 {
		for _, s := range specs {
			if _, ok := yamlEntry.Values.Base[s.Name]; ok {
				sources[s.Name] = "pipelines.yaml values.base"
			}
		}
		if err := applyValueOverlay(elem, specs, yamlEntry.Values.Base, reg.Name); err != nil {
			return nil, err
		}
	}

	// Layer 3: pipelines.yaml targets.<target>.values.
	if yamlEntry != nil && target != "" {
		if t, ok := yamlEntry.Targets[target]; ok && len(t.Values) > 0 {
			for _, s := range specs {
				if _, ok := t.Values[s.Name]; ok {
					sources[s.Name] = fmt.Sprintf("pipelines.yaml targets.%s.values", target)
				}
			}
			if err := applyValueOverlay(elem, specs, t.Values, reg.Name); err != nil {
				return nil, err
			}
		}
	}

	// Layer 4: matched trigger spec's values: block.
	if yamlEntry != nil && triggerSource != "" {
		if tv := yamlEntry.TriggerValues(triggerSource); len(tv) > 0 {
			for _, s := range specs {
				if _, ok := tv[s.Name]; ok {
					sources[s.Name] = fmt.Sprintf("pipelines.yaml on.%s.values", triggerSource)
				}
			}
			if err := applyValueOverlay(elem, specs, tv, reg.Name); err != nil {
				return nil, err
			}
		}
	}

	out := make([]ConfigField, 0, len(specs))
	for _, s := range specs {
		fv := elem.FieldByIndex(s.Field.Index).Interface()
		src := sources[s.Name]
		if src == "" {
			src = "not set"
		}
		out = append(out, ConfigField{
			Name:     s.Name,
			GoField:  s.Field.Name,
			TypeName: s.Field.Type.String(),
			Value:    fv,
			Source:   src,
			Required: s.Required,
		})
	}
	return out, nil
}

// SecretField is one entry in the result of
// InspectPipelineSecrets: the declared secret + (when a SecretResolver
// is installed on ctx) the resolution outcome.
type SecretField struct {
	// Name is the secret name as the pipeline asks for it.
	Name string
	// GoField, when non-empty, is the Go struct field on Secrets()
	// that maps to this secret name. Empty for secrets declared
	// only in pipelines.yaml secrets: list.
	GoField string
	// Required reports whether the declaration marked this secret
	// required. Required secrets fail the run at fail-fast time
	// when the resolver can't find them.
	Required bool
	// DeclaredIn is "pipelines.yaml secrets:" when the secret came
	// from the yaml list, "Secrets() struct" when it came from the
	// pipeline's typed Secrets struct.
	DeclaredIn string
	// SourceName is the sources.yaml entry the resolver is bound
	// to for this run (e.g. "team-vault"). Empty when no source
	// binding is in effect.
	SourceName string
	// Resolved reports whether the resolver returned a value.
	// Set when a SecretResolver was installed on ctx; left at the
	// zero value (false) when inspection ran without one.
	Resolved bool
	// Note carries the resolution error message (or "not resolved
	// yet" when no resolver is installed). Empty when Resolved is
	// true and no error occurred.
	Note string
}

// InspectPipelineSecrets enumerates the pipeline's declared secrets
// and (when ctx carries a SecretResolver) attempts each one. The
// returned entries union the pipelines.yaml secrets: list and the
// pipeline's Secrets() struct fields, matching the same precedence
// ResolvePipelineSecrets uses (struct fields can declare required).
//
// sourceName is informational only: pass the resolved sources.yaml
// entry name (e.g. opts.PipelineYAML.Targets[opts.Target].Source or
// the sources.yaml default) so the per-entry SourceName column
// renders for the operator. Empty is fine; the column reports empty.
//
// Returns (nil, nil) when the pipeline declares no secrets at all.
func InspectPipelineSecrets(ctx context.Context, reg *Registration, yamlEntry *pipelines.Pipeline, sourceName string) ([]SecretField, error) {
	if reg == nil || reg.instance == nil {
		return nil, nil
	}
	// Gather declarations from both sources.
	type entry struct {
		name       string
		goField    string
		required   bool
		declaredIn string
	}
	var entries []entry
	seen := map[string]int{}

	if yamlEntry != nil {
		for _, s := range yamlEntry.Secrets {
			if s.Name == "" {
				continue
			}
			entries = append(entries, entry{
				name:       s.Name,
				required:   s.Required || (!s.Required && !s.Optional),
				declaredIn: "pipelines.yaml secrets:",
			})
			seen[s.Name] = len(entries) - 1
		}
	}

	// Pull struct-declared secrets via reflection.
	p := reg.instance()
	if sp, ok := p.(SecretsProvider); ok {
		raw := sp.Secrets()
		if raw != nil {
			rv := reflect.ValueOf(raw)
			if rv.Kind() == reflect.Pointer && !rv.IsNil() && rv.Elem().Kind() == reflect.Struct {
				specs, err := swtags.Parse(rv.Type())
				if err == nil {
					for _, s := range specs {
						if idx, ok := seen[s.Name]; ok {
							// Struct-declared required tightens the yaml entry.
							if s.Required {
								entries[idx].required = true
							}
							if entries[idx].goField == "" {
								entries[idx].goField = s.Field.Name
							}
							continue
						}
						entries = append(entries, entry{
							name:       s.Name,
							goField:    s.Field.Name,
							required:   s.Required,
							declaredIn: "Secrets() struct",
						})
						seen[s.Name] = len(entries) - 1
					}
				}
			}
		}
	}

	if len(entries) == 0 {
		return nil, nil
	}

	resolver := secretResolverFromContext(ctx)
	out := make([]SecretField, 0, len(entries))
	for _, e := range entries {
		sf := SecretField{
			Name:       e.name,
			GoField:    e.goField,
			Required:   e.required,
			DeclaredIn: e.declaredIn,
			SourceName: sourceName,
		}
		if resolver != nil {
			val, _, err := resolver.Resolve(ctx, e.name)
			switch {
			case err == nil && val != "":
				sf.Resolved = true
			case errors.Is(err, ErrSecretMissing):
				sf.Note = "missing"
			case err != nil:
				sf.Note = err.Error()
			default:
				sf.Resolved = true
			}
		} else {
			sf.Note = "not resolved yet"
		}
		out = append(out, sf)
	}
	return out, nil
}
