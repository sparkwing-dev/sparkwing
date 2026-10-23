package controller

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

const (
	maxLogValue   = 256
	maxLogMessage = 1024
)

// CredentialSafeLogHandler fingerprints Sparkwing credentials and bounds strings
// before records reach any log sink, including telemetry. Install it before attaching attributes.
func CredentialSafeLogHandler(handler slog.Handler) slog.Handler {
	if _, ok := handler.(*credentialLogHandler); ok {
		return handler
	}
	return &credentialLogHandler{handler}
}

type credentialLogHandler struct{ slog.Handler }

func (h *credentialLogHandler) Handle(ctx context.Context, record slog.Record) error {
	safe := slog.NewRecord(record.Time, record.Level, truncateLogValue(capability.FormatLogValue(record.Message), maxLogMessage), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		safe.AddAttrs(credentialSafeAttr(attr))
		return true
	})
	return h.Handler.Handle(ctx, safe)
}

func (h *credentialLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		safe[i] = credentialSafeAttr(attr)
	}
	return &credentialLogHandler{h.Handler.WithAttrs(safe)}
}

func (h *credentialLogHandler) WithGroup(name string) slog.Handler {
	return &credentialLogHandler{h.Handler.WithGroup(formatLogValue(name))}
}

func credentialSafeAttr(attr slog.Attr) slog.Attr {
	attr.Key = formatLogValue(attr.Key)
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		text := formatLogValue(value.String())
		if attr.Key == "token_prefixes" {
			// The machine id identifies a removal even when its prefix list is clipped.
			text = clip(text)
		}
		value = slog.StringValue(text)
	case slog.KindGroup:
		group := value.Group()
		safe := make([]slog.Attr, len(group))
		for i, child := range group {
			safe[i] = credentialSafeAttr(child)
		}
		value = slog.GroupValue(safe...)
	case slog.KindAny:
		switch v := value.Any().(type) {
		case nil:
		case []byte:
			value = slog.StringValue(formatLogValue(string(v)))
		case []string:
			safe := make([]string, len(v))
			for i, item := range v {
				safe[i] = formatLogValue(item)
			}
			value = slog.AnyValue(safe)
		default:
			// Render opaque values once so errors and custom marshalers cannot bypass redaction.
			value = slog.StringValue(formatLogValue(fmt.Sprint(v)))
		}
	}
	attr.Value = value
	return attr
}

func formatLogValue(value string) string {
	return truncateLogValue(capability.FormatLogValue(value), maxLogValue)
}

func truncateLogValue(value string, limit int) string {
	const marker = "[truncated]"
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit-len(marker)], "") + marker
}

var pathCredential = regexp.MustCompile(`(?:` + strings.Join(capability.CredentialPrefixes(), "|") + `)[A-Za-z0-9_-]+`)
