package sparkwing

import (
	"context"
	"errors"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/swtags"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// SecretField is one entry in the result of
// InspectPipelineSecrets: the declared secret + (when a SecretResolver
// is installed on ctx) the resolution outcome.
type SecretField struct {
	// Name is the secret name as the pipeline asks for it.
	Name string
	// GoField, when non-empty, is the Go struct field on Secrets()
	// that maps to this secret name. Empty for secrets declared
	// only in sparkwing.yaml secrets: list.
	GoField string
	// Required reports whether the declaration marked this secret
	// required. Required secrets fail the run at fail-fast time
	// when the resolver can't find them.
	Required bool
	// DeclaredIn is "sparkwing.yaml secrets:" when the secret came
	// from the yaml list, "Secrets() struct" when it came from the
	// pipeline's typed Secrets struct.
	DeclaredIn string
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
// returned entries union the sparkwing.yaml secrets: list and the
// pipeline's Secrets() struct fields, matching the same precedence
// ResolvePipelineSecrets uses (struct fields can declare required).
//
// Returns (nil, nil) when the pipeline declares no secrets at all.
func InspectPipelineSecrets(ctx context.Context, reg *Registration, yamlEntry *pipelines.Pipeline) ([]SecretField, error) {
	if reg == nil || reg.instance == nil {
		return nil, nil
	}
	type entry struct {
		name       string
		goField    string
		required   bool
		declaredIn string
	}
	_ = yamlEntry // YAML-side secrets declarations are gone; provider is the source of truth.
	var entries []entry
	seen := map[string]int{}

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
