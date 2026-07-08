package sparkwingruntime

import (
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/swtags"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// ReflectSecretsField returns the SecretsField a pipeline declares
// via its Secrets() provider, reflecting on the returned struct's
// `sw:"NAME,required|optional"` tags. Returns nil when the pipeline
// doesn't implement SecretsProvider or the provider returns nil.
//
// Required defaults to true when neither flag is set, matching the
// historical declaration semantics.
func ReflectSecretsField(reg *sparkwing.Registration) pipelines.SecretsField {
	if reg == nil {
		return nil
	}
	p := reg.Instance()
	if p == nil {
		return nil
	}
	sp, ok := p.(sparkwing.SecretsProvider)
	if !ok {
		return nil
	}
	raw := sp.Secrets()
	if raw == nil {
		return nil
	}
	rv := reflect.ValueOf(raw)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return nil
	}
	specs, err := swtags.Parse(rv.Type())
	if err != nil {
		return nil
	}
	out := make(pipelines.SecretsField, 0, len(specs))
	for _, s := range specs {
		entry := pipelines.SecretEntry{Name: s.Name}
		switch {
		case s.Required:
			entry.Required = true
		case s.Optional:
			entry.Optional = true
		default:
			entry.Required = true
		}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
