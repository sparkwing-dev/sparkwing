// Package mailer sends transactional email, such as team invitations,
// through Amazon SES or, when no mail service is configured, a log line
// that records the attempt without the recipient or the message body.
package mailer

import (
	"context"
	"errors"
	"log/slog"
	"strings"
)

// Message is one outbound email with a plain-text and an HTML body.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Mailer delivers a Message to its recipient.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// Log is the Mailer used when no mail service is configured. It records
// the subject of each message and never delivers it.
type Log struct {
	// Logger receives the record; nil means slog.Default().
	Logger *slog.Logger
}

// Send logs that m was not sent. The recipient is omitted because an
// address is personal data, and the body because it can carry a bearer
// link, such as an invitation accept URL.
func (l Log) Send(ctx context.Context, m Message) error {
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.InfoContext(ctx, "mail not sent; no mail service configured", "subject", m.Subject)
	return nil
}

// ErrorClass names why a send failed as one fixed word, so a caller can log
// it. A provider's error text can quote the recipient address, which is
// personal data, so callers log the class instead of the error.
func ErrorClass(err error) string {
	var api interface{ ErrorCode() string }
	var status interface{ HTTPStatusCode() int }
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &status) && status.HTTPStatusCode() >= 500:
		return "unavailable"
	case errors.As(err, &api):
		code := api.ErrorCode()
		if strings.Contains(code, "Throttl") || strings.Contains(code, "TooManyRequests") || strings.Contains(code, "LimitExceeded") {
			return "throttled"
		}
		return "rejected"
	default:
		return "unavailable"
	}
}
