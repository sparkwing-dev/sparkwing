// Package mailer sends transactional email, such as team invitations,
// through Amazon SES or, when no mail service is configured, a log line
// that records the attempt without the recipient or the message body.
package mailer

import (
	"context"
	"log/slog"
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
