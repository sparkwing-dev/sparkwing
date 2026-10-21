package sparkwing

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type planningDiagnostics struct {
	ctx    context.Context
	args   map[string]string
	values []diagnosticSecret
}

type diagnosticSecret struct {
	value string
	list  bool
}

func (v diagnosticSecret) register(masker *secrets.Masker) {
	if v.list {
		masker.RegisterList(v.value)
	} else {
		masker.Register(v.value)
	}
}

type (
	planningDiagnosticsKey  struct{}
	planningMaskServicesKey struct{}
)

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
			kind := field.Type.String()
			if field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.String {
				kind = "[]string"
			}
			d.collect(inputSchema{Fields: []inputField{{Name: name, Type: kind, Default: field.Tag.Get("default"), Secret: boolTag(field.Tag.Get("secret"))}}})
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
			secret := diagnosticSecret{value: value, list: field.Type == "[]string"}
			if value != "" && !slices.Contains(d.values, secret) {
				d.values = append(d.values, secret)
				if secret.list && d.ctx != nil {
					if services, ok := d.ctx.Value(planningMaskServicesKey{}).(sdkserve.Services); ok {
						for _, part := range secrets.ListFragments(value) {
							if err := services.Mask(d.ctx, part); err != nil {
								panic(err)
							}
						}
					}
				}
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
		value.register(masker)
	}
	code := source.Code
	if masker.ContainsSecret(string(code)) {
		code = fallback
	}
	return &apierr.Error{Code: code, Message: masker.Mask(prefix + source.Message), Hint: masker.Mask(source.Hint), Details: masker.MaskDetails(source.Details)}
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
