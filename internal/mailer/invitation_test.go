package mailer_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/mailer"
)

func mustInvitation(t *testing.T, inv mailer.Invitation) mailer.Message {
	t.Helper()
	m, err := mailer.InvitationMessage("invitee@example.com", inv)
	if err != nil {
		t.Fatalf("InvitationMessage: %v", err)
	}
	return m
}

func testInvitation() mailer.Invitation {
	return mailer.Invitation{
		InviterName: "Ada Lovelace",
		TeamName:    "Platform Ops",
		Role:        "maintainer",
		AcceptURL:   "https://sparkwing.example/invitations/accept?token=tok123",
		// 23:30 in UTC-7 is already the next day in UTC.
		ExpiresAt: time.Date(2026, 9, 28, 23, 30, 0, 0, time.FixedZone("PDT", -7*3600)),
	}
}

func TestInvitationMessageStatesTheInvitation(t *testing.T) {
	inv := testInvitation()
	m := mustInvitation(t, inv)

	if m.To != "invitee@example.com" {
		t.Errorf("To = %q", m.To)
	}
	if m.Subject != "Ada Lovelace invited you to Platform Ops on Sparkwing" {
		t.Errorf("Subject = %q", m.Subject)
	}
	for _, body := range []struct{ name, s string }{{"text", m.Text}, {"html", m.HTML}} {
		for _, want := range []string{inv.InviterName, inv.TeamName, inv.Role, inv.AcceptURL, "29 September 2026", "ignore this email"} {
			if !strings.Contains(body.s, want) {
				t.Errorf("%s body lacks %q", body.name, want)
			}
		}
	}
}

func TestInvitationMessageEscapesHTML(t *testing.T) {
	inv := testInvitation()
	inv.TeamName = "<script>alert(1)</script>"
	m := mustInvitation(t, inv)

	if strings.Contains(m.HTML, "<script>") {
		t.Fatalf("HTML carries a raw script tag: %s", m.HTML)
	}
	if !strings.Contains(m.HTML, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("HTML lacks the escaped team name: %s", m.HTML)
	}
}

func TestInvitationSubjectHasNoLineBreaks(t *testing.T) {
	inv := testInvitation()
	inv.InviterName = "Mallory\r\nBcc: x@y"
	m := mustInvitation(t, inv)

	if strings.ContainsAny(m.Subject, "\r\n") {
		t.Fatalf("Subject carries a line break: %q", m.Subject)
	}
}
