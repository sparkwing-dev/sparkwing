package sparkwing

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
	patterns := []string{}
	for _, value := range d.values {
		encoded, _ := json.Marshal(value)
		for _, form := range []string{
			value, string(encoded[1 : len(encoded)-1]),
			strconv.Quote(value)[1 : len(strconv.Quote(value))-1],
			strconv.QuoteToASCII(value)[1 : len(strconv.QuoteToASCII(value))-1],
			url.QueryEscape(value), url.PathEscape(value),
			base64.StdEncoding.EncodeToString([]byte(value)),
			base64.RawStdEncoding.EncodeToString([]byte(value)),
			base64.URLEncoding.EncodeToString([]byte(value)),
			base64.RawURLEncoding.EncodeToString([]byte(value)),
		} {
			patterns = append(patterns, form)
		}
	}
	slices.SortFunc(patterns, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(patterns))
	for _, pattern := range patterns {
		pairs = append(pairs, pattern, "***")
	}
	replace := strings.NewReplacer(pairs...).Replace
	mask := func(text string) string {
		masked := replace(text)
		if slices.ContainsFunc(patterns, func(pattern string) bool { return strings.Contains(masked, pattern) }) {
			return ""
		}
		return masked
	}
	out := &apierr.Error{Code: source.Code, Message: mask(prefix + source.Message), Hint: mask(source.Hint)}
	if source.Details != nil {
		raw, err := json.Marshal(source.Details)
		if err != nil {
			return out
		}
		var details any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&details); err == nil {
			out.Details = sanitizeDiagnosticValue(details, mask).(map[string]any)
		}
	}
	return out
}

func sanitizeDiagnosticValue(value any, mask func(string) string) any {
	switch value := value.(type) {
	case string:
		return mask(value)
	case json.Number:
		if masked := mask(string(value)); masked != string(value) {
			return masked
		}
	case bool, nil:
		raw, _ := json.Marshal(value)
		if masked := mask(string(raw)); masked != string(raw) {
			return masked
		}
	case []any:
		for i, child := range value {
			value[i] = sanitizeDiagnosticValue(child, mask)
		}
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			out[mask(key)] = sanitizeDiagnosticValue(child, mask)
		}
		return out
	}
	return value
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
