package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// CredentialSafeLogHandler fingerprints Sparkwing credentials before records
// reach any log sink, including telemetry. Install it before attaching attributes.
func CredentialSafeLogHandler(handler slog.Handler) slog.Handler {
	if _, ok := handler.(*credentialLogHandler); ok {
		return handler
	}
	return &credentialLogHandler{handler}
}

type credentialLogHandler struct{ slog.Handler }

func (h *credentialLogHandler) Handle(ctx context.Context, record slog.Record) error {
	safe := slog.NewRecord(record.Time, record.Level, formatAuditValue(record.Message), record.PC)
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
	return &credentialLogHandler{h.Handler.WithGroup(formatAuditValue(name))}
}

func credentialSafeAttr(attr slog.Attr) slog.Attr {
	attr.Key = formatAuditValue(attr.Key)
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		text := formatAuditValue(value.String())
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
			value = slog.StringValue(formatAuditValue(string(v)))
		case []string:
			safe := make([]string, len(v))
			for i, item := range v {
				safe[i] = formatAuditValue(item)
			}
			value = slog.AnyValue(safe)
		default:
			// Render opaque values once so errors and custom marshalers cannot bypass redaction.
			value = slog.StringValue(formatAuditValue(fmt.Sprint(v)))
		}
	}
	attr.Value = value
	return attr
}

func formatAuditValue(value string) string {
	// Most log fields contain no prefix. Avoid the regexp and allocations there.
	if !strings.Contains(value, "sw") {
		return value
	}
	return pathCredential.ReplaceAllStringFunc(value, func(credential string) string {
		// A stored capability prefix is a public identifier, not a bearer.
		if len(credential) == 16 {
			return credential
		}
		digest := sha256.Sum256([]byte(credential))
		return "[credential:" + hex.EncodeToString(digest[:8]) + "]"
	})
}

var pathCredential = regexp.MustCompile(`(?:` + strings.Join(capability.CredentialPrefixes(), "|") + `)[A-Za-z0-9_-]+`)
