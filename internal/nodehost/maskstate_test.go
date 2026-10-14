package nodehost

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

var maskingLogHandlers = map[string]func(io.Writer) slog.Handler{
	"text": func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, nil) },
	"json": func(w io.Writer) slog.Handler { return slog.NewJSONHandler(w, nil) },
}

type maskingLogValue func() slog.Value

func (f maskingLogValue) LogValue() slog.Value { return f() }

func TestMaskLoggerResolvesValuesOnce(t *testing.T) {
	for format, handler := range maskingLogHandlers {
		for _, second := range []slog.Value{slog.StringValue("private-token"), slog.GroupValue()} {
			t.Run(format+"/"+second.Kind().String(), func(t *testing.T) {
				var out strings.Builder
				masker := secrets.NewMasker()
				masker.Register("private-token")
				calls := 0
				value := maskingLogValue(func() slog.Value {
					calls++
					if calls == 1 {
						return slog.StringValue("healthy")
					}
					return second
				})
				maskLogger(slog.New(handler(&out)), masker).Info("status", "state", value)
				if calls != 1 {
					t.Errorf("LogValue called %d times, want 1", calls)
				}
				if got := out.String(); strings.Contains(got, "private-token") || !strings.Contains(got, "healthy") {
					t.Errorf("resolved state lost or secret leaked: %s", got)
				}
			})
		}
	}
}

func TestMaskLoggerMasksBoundAttrsWhenHandled(t *testing.T) {
	for format, handler := range maskingLogHandlers {
		t.Run(format, func(t *testing.T) {
			var out strings.Builder
			masker := secrets.NewMasker()
			calls := 0
			value := maskingLogValue(func() slog.Value {
				calls++
				return slog.StringValue("late-token")
			})
			logger := maskLogger(slog.New(handler(&out)), masker).
				With("root", "late-token").WithGroup("outer").
				With(slog.Group("inner", "token", value)).
				WithGroup("leaf").With("state", "healthy").WithGroup("")
			if calls != 0 {
				t.Errorf("LogValue called %d times before logging", calls)
			}
			masker.Register("late-token")
			logger.Info("status", "record", "done")
			want := `root=*** outer.inner.token=*** outer.leaf.state=healthy outer.leaf.record=done`
			if format == "json" {
				want = `"root":"***","outer":{"inner":{"token":"***"},"leaf":{"state":"healthy","record":"done"}}`
			}
			if got := out.String(); strings.Contains(got, "late-token") || !strings.Contains(got, want) {
				t.Errorf("bound attributes leaked or changed structure: %s", got)
			}
			if calls != 1 {
				t.Errorf("LogValue called %d times, want 1", calls)
			}
		})
	}
}

type maskingEncodedValue struct{}

func (maskingEncodedValue) String() string               { return "healthy" }
func (maskingEncodedValue) MarshalJSON() ([]byte, error) { return []byte(`"private-token"`), nil }
func (maskingEncodedValue) MarshalText() ([]byte, error) { return []byte("private-token"), nil }

func TestMaskLoggerMasksEncodedValues(t *testing.T) {
	for format, handler := range maskingLogHandlers {
		for _, tc := range []struct {
			name       string
			value      any
			secret     string
			text, json string
		}{
			{"bytes", []byte("private-token"), "private-token", `value="\"***\""`, `"value":"\"***\""`},
			{"marshaler", maskingEncodedValue{}, "private-token", `value="\"***\""`, `"value":"\"***\""`},
			{"escaped-json", json.RawMessage(`"\u0070rivate-token"`), "private-token", `value="\"***\""`, `"value":"\"***\""`},
			{"text-field", struct {
				PrivateToken string `json:"state"`
			}{"healthy"}, "PrivateToken", `value="{\"state\":\"healthy\"}"`, `"value":"{\"state\":\"healthy\"}"`},
			{"struct-secret", struct{ Token string }{"private-token"}, "private-token", `value="{\"Token\":\"***\"}"`, `"value":"{\"Token\":\"***\"}"`},
			{"duration", time.Second, "private-token", "value=1s", `"value":1000000000`},
			{"float", float64(1000000000), "private-token", "value=1e+09", `"value":1000000000`},
			{"time", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "private-token", "value=2026-01-02T03:04:05.000Z", `"value":"2026-01-02T03:04:05Z"`},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				var out strings.Builder
				masker := secrets.NewMasker()
				masker.Register(tc.secret)
				maskLogger(slog.New(handler(&out)), masker).Info("status", "value", tc.value)
				got := out.String()
				if strings.Contains(got, tc.secret) || strings.Contains(got, base64.StdEncoding.EncodeToString([]byte(tc.secret))) {
					t.Errorf("encoded secret leaked: %s", got)
				}
				want := tc.text
				if format == "json" {
					want = tc.json
				}
				if !strings.Contains(got, want) {
					t.Errorf("lost encoded value %q: %s", want, got)
				}
			})
		}
	}
}

func TestMaskLoggerMasksScalars(t *testing.T) {
	for format, handler := range maskingLogHandlers {
		for _, tc := range []struct {
			attr   slog.Attr
			secret string
		}{
			{slog.Int("int", 1234567), "1234567"},
			{slog.Uint64("uint", 1234567), "1234567"},
			{slog.Float64("float", 1234567), "1234567"},
			{slog.Duration("duration", time.Duration(1234567)), "1234567"},
			{slog.Time("time", time.Date(2026, 1, 2, 3, 4, 5, 123456700, time.UTC)), "1234567"},
			{slog.Bool("bool", true), "true"},
		} {
			t.Run(format+"/"+tc.attr.Key, func(t *testing.T) {
				var out strings.Builder
				masker := secrets.NewMasker()
				masker.Register(tc.secret)
				maskLogger(slog.New(handler(&out)), masker).Info("status", tc.attr)
				if got := out.String(); strings.Contains(got, tc.secret) || !strings.Contains(got, "***") {
					t.Errorf("scalar secret leaked or was not masked: %s", got)
				}
			})
		}
	}
}

func TestMaskLoggerMasksAttributeKeys(t *testing.T) {
	for format, handler := range maskingLogHandlers {
		t.Run(format, func(t *testing.T) {
			var out strings.Builder
			masker := secrets.NewMasker()
			logger := maskLogger(slog.New(handler(&out)), masker).With("private-token-bound", "bound")
			masker.Register("private-token")
			logger.Info("status", "private-token-record", "record",
				slog.Group("private-token-group", "private-token-nested", "nested"))
			got := out.String()
			if strings.Contains(got, "private-token") {
				t.Errorf("attribute key leaked: %s", got)
			}
			for _, key := range []string{"***-bound", "***-record", "***-group", "***-nested"} {
				if !strings.Contains(got, key) {
					t.Errorf("masked key %q missing: %s", key, got)
				}
			}
		})
	}
}

func TestMaskLoggerMasksGroupNamesWhenHandled(t *testing.T) {
	for format, handler := range maskingLogHandlers {
		t.Run(format, func(t *testing.T) {
			var out strings.Builder
			masker := secrets.NewMasker()
			logger := maskLogger(slog.New(handler(&out)), masker).WithGroup("private-token").With("state", "healthy")
			masker.Register("private-token")
			logger.Info("status")
			want := `***.state=healthy`
			if format == "json" {
				want = `"***":{"state":"healthy"}`
			}
			if got := out.String(); strings.Contains(got, "private-token") || !strings.Contains(got, want) {
				t.Errorf("group name leaked or lost attributes: %s", got)
			}
		})
	}
}

type maskingJSONObject struct{}

func (maskingJSONObject) MarshalJSON() ([]byte, error) {
	return []byte(`{"state":"healthy"}`), nil
}

func TestMaskLoggerPreservesBenignJSONContent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  any
	}{
		{"marshaler", maskingJSONObject{}, `{"state":"healthy"}`},
		{"array", [2]string{"alpha", "beta"}, `["alpha","beta"]`},
		{"float", 1.5, 1.5},
		{"struct", struct{ State string }{"healthy"}, `{"State":"healthy"}`},
		{"logvaluer", maskingLogValue(func() slog.Value {
			return slog.AnyValue(maskingJSONObject{})
		}), `{"state":"healthy"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			masker := secrets.NewMasker()
			masker.Register("private-token")
			maskLogger(slog.New(slog.NewJSONHandler(&out, nil)), masker).Info("status", "value", tc.value)
			var record map[string]any
			if err := json.Unmarshal([]byte(out.String()), &record); err != nil {
				t.Fatal(err)
			}
			if got := record["value"]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("value = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestMaskLoggerPreservesBenignAttrs(t *testing.T) {
	for format, handler := range maskingLogHandlers {
		t.Run(format, func(t *testing.T) {
			var out strings.Builder
			logger := maskLogger(slog.New(handler(&out)), secrets.NewMasker()).With(slog.Attr{})
			logger.Info("status", slog.Attr{}, slog.Group("empty"),
				slog.Int("count", 3), slog.Bool("ok", true), slog.Any("value", nil))
			want := `msg=status count=3 ok=true value=null`
			if format == "json" {
				want = `"msg":"status","count":3,"ok":true,"value":"null"}`
			}
			if got := out.String(); !strings.Contains(got, want) {
				t.Errorf("benign attributes changed: %s", got)
			}
		})
	}
}

type maskingTextValue struct{ maskingJSONObject }

func (maskingTextValue) String() string               { return "healthy" }
func (maskingTextValue) MarshalText() ([]byte, error) { return []byte("private-token"), nil }

type maskingError struct{}

func (maskingError) Error() string              { return "private-token" }
func (maskingError) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "healthy") }

type maskingErrorValue func() string

func (f maskingErrorValue) Error() string { return f() }

type maskingJSONFailure struct{ panic bool }

func (maskingJSONFailure) String() string { return "healthy" }
func (v maskingJSONFailure) MarshalJSON() ([]byte, error) {
	if v.panic {
		panic("private-token: unregistered-diagnostic")
	}
	return nil, errors.New("private-token: unregistered-diagnostic")
}

func TestMaskLoggerMasksHandlerEncodings(t *testing.T) {
	panicValue := func() string { panic("private-token: unregistered-diagnostic") }
	for format, handler := range maskingLogHandlers {
		for _, tc := range []struct {
			name       string
			value      any
			text, json string
		}{
			{"text-marshaler", maskingTextValue{}, `{\"state\":\"healthy\"}`, `{\"state\":\"healthy\"}`},
			{"error", maskingError{}, "***", "***"},
			{"json-error", maskingJSONFailure{}, "!masked: unrenderable", "!masked: unrenderable"},
			{"json-panic", maskingJSONFailure{panic: true}, "!masked: unrenderable", "!masked: unrenderable"},
			{"error-panic", maskingErrorValue(panicValue), "!masked: unrenderable", "!masked: unrenderable"},
			{"string-panic", maskingStatefulString(panicValue), "!masked: unrenderable", "!masked: unrenderable"},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("encoding panic escaped slog: %v", p)
					}
				}()
				var out strings.Builder
				masker := secrets.NewMasker()
				masker.Register("private-token")
				maskLogger(slog.New(handler(&out)), masker).Info("status", "value", tc.value, "after", "done")
				got, want := out.String(), tc.text
				if format == "json" {
					want = tc.json
					if !json.Valid([]byte(got)) {
						t.Errorf("invalid JSON record: %s", got)
					}
				}
				if strings.Contains(got, "private-token") || !strings.Contains(got, want) || !strings.Contains(got, "status") || !strings.Contains(got, "done") {
					t.Errorf("record lost, encoding changed or secret leaked: %s", got)
				}
				if strings.Contains(got, "unregistered-diagnostic") {
					t.Errorf("encoding diagnostic leaked: %s", got)
				}
			})
		}
	}
}

type maskingStatefulString func() string

func (f maskingStatefulString) String() string { return f() }

type maskingStatefulJSON func() string

func (f maskingStatefulJSON) MarshalJSON() ([]byte, error) { return []byte(`"` + f() + `"`), nil }

func TestMaskLoggerEncodesStatefulValuesOncePerRecord(t *testing.T) {
	for format, handler := range maskingLogHandlers {
		t.Run(format, func(t *testing.T) {
			var out strings.Builder
			masker := secrets.NewMasker()
			masker.Register("private-token")
			calls := 0
			next := func() string {
				calls++
				if calls == 1 {
					return "healthy"
				}
				return "private-token"
			}
			var value any = maskingStatefulString(next)
			if format == "json" {
				value = maskingStatefulJSON(next)
			}
			logger := maskLogger(slog.New(handler(&out)), masker)
			logger.Info("first", "value", value)
			logger.Info("second", "value", value)
			if calls != 2 {
				t.Errorf("encoding called %d times for two records, want 2", calls)
			}
			if got := out.String(); strings.Contains(got, "private-token") || !strings.Contains(got, "healthy") || !strings.Contains(got, "***") {
				t.Errorf("stateful encoding lost or secret leaked: %s", got)
			}
		})
	}
}
