package nodehost

import (
	"encoding/base64"
	"io"
	"log/slog"
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
			name   string
			value  any
			secret string
			want   string
		}{
			{"bytes", []byte("private-token"), "private-token", "***"},
			{"marshaler", maskingEncodedValue{}, "private-token", "healthy"},
			{"duration", time.Second, "1000000000", "1s"},
			{"float", float64(1000000000), "1000000000", "1e+09"},
			{"time", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "2026-01-02T03:04:05Z", "2026-01-02 03:04:05 +0000 UTC"},
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
				if !strings.Contains(got, tc.want) {
					t.Errorf("lost safe value %q: %s", tc.want, got)
				}
			})
		}
	}
}

func TestMaskLoggerPreservesBenignAttrs(t *testing.T) {
	for format, handler := range maskingLogHandlers {
		t.Run(format, func(t *testing.T) {
			var out strings.Builder
			logger := maskLogger(slog.New(handler(&out)), secrets.NewMasker()).With(slog.Attr{})
			logger.Info("status", slog.Attr{}, slog.Group("empty"),
				slog.Int("count", 3), slog.Bool("ok", true), slog.Any("value", nil))
			want := `msg=status count=3 ok=true value=<nil>`
			if format == "json" {
				want = `"msg":"status","count":3,"ok":true,"value":null}`
			}
			if got := out.String(); !strings.Contains(got, want) {
				t.Errorf("benign attributes changed: %s", got)
			}
		})
	}
}
