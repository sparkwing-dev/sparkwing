package mailer_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/mailer"
)

func TestLogSendOmitsBody(t *testing.T) {
	var buf bytes.Buffer
	l := mailer.Log{Logger: slog.New(slog.NewTextHandler(&buf, nil))}
	m := mailer.Message{
		To:      "invitee@example.com",
		Subject: "Ada invited you to Ops on Sparkwing",
		Text:    "https://sparkwing.example/accept?token=secret-text",
		HTML:    "https://sparkwing.example/accept?token=secret-html",
	}
	if err := l.Send(context.Background(), m); err != nil {
		t.Fatalf("Send: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, m.To) || !strings.Contains(out, m.Subject) {
		t.Fatalf("log line lacks recipient or subject: %s", out)
	}
	if strings.Contains(out, "secret-text") || strings.Contains(out, "secret-html") {
		t.Fatalf("log line leaks the body: %s", out)
	}
}
