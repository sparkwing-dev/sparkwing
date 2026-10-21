package sparkwing

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type planningDiagnostics struct {
	args   map[string]string
	values []string
}

type planningDiagnosticsKey struct{}

func (d *planningDiagnostics) collectJob(job any) {
	_, t := embeddedArgs(job)
	if d == nil || t == nil {
		return
	}
	d.collectType(t, 0)
}

func (d *planningDiagnostics) collectType(t reflect.Type, depth int) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || depth > maxEmbedDepth {
		return
	}
	for i := range t.NumField() {
		field := t.Field(i)
		name, flagged := field.Tag.Lookup("flag")
		if field.Anonymous && !flagged {
			d.collectType(field.Type, depth+1)
			continue
		}
		if name != "" && name != ",extra" && field.IsExported() {
			d.collect(inputSchema{Fields: []inputField{{Name: name, Default: field.Tag.Get("default"), Secret: boolTag(field.Tag.Get("secret"))}}})
		}
	}
}

func (d *planningDiagnostics) collect(schema inputSchema) {
	if d == nil {
		return
	}
	for _, field := range schema.Fields {
		if !field.Secret || field.isExtraBag {
			continue
		}
		for _, value := range []string{field.Default, d.args[field.Name]} {
			if value != "" && !slices.Contains(d.values, value) {
				d.values = append(d.values, value)
			}
		}
	}
}

func (d *planningDiagnostics) sanitize(failure error, fallback apierr.Code, prefix string) error {
	if failure == nil {
		return nil
	}
	var source *apierr.Error
	if !errors.As(failure, &source) {
		source = sdkError(fallback, failure.Error())
	}
	masker := secrets.NewMasker()
	for _, value := range d.values {
		masker.Register(value)
	}
	code := source.Code
	if masker.ContainsSecret(string(code)) {
		code = fallback
	}
	return &apierr.Error{Code: code, Message: masker.Mask(prefix + source.Message), Hint: masker.Mask(source.Hint), Details: secrets.MaskDetails(source.Details, masker.Mask)}
}

func planningFailure(value any) error {
	if failure, ok := value.(error); ok {
		return failure
	}
	return fmt.Errorf("%v", value)
}

func withPlanningDiagnostics(ctx context.Context, d *planningDiagnostics) context.Context {
	return context.WithValue(ctx, planningDiagnosticsKey{}, d)
}
