package mailer

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// Invitation carries the facts a team invitation email states.
type Invitation struct {
	InviterName string
	TeamName    string
	Role        string
	AcceptURL   string
	ExpiresAt   time.Time
}

const expiryLayout = "2 January 2006"

var invitationHTML = template.Must(template.New("invitation").Parse(`<!DOCTYPE html>
<html lang="en">
<body style="margin:0;padding:24px;background:#ffffff;color:#1a1a1a;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;font-size:15px;line-height:1.5;">
<div style="max-width:560px;">
<p style="margin:0 0 16px;">{{.InviterName}} invited you to join the team <strong>{{.TeamName}}</strong> on Sparkwing as {{.Role}}.</p>
<p style="margin:0 0 16px;"><a href="{{.AcceptURL}}" style="display:inline-block;padding:10px 16px;background:#1a1a1a;color:#ffffff;text-decoration:none;border-radius:4px;">Accept invitation</a></p>
<p style="margin:0 0 16px;">If the button does not work, open this link:<br><a href="{{.AcceptURL}}" style="color:#1a1a1a;word-break:break-all;">{{.AcceptURL}}</a></p>
<p style="margin:0 0 16px;">This invitation expires on {{.Expires}} (7 days from when it was sent).</p>
<p style="margin:0;color:#555555;">If you did not expect this invitation, you can ignore this email.</p>
</div>
</body>
</html>
`))

// InvitationMessage renders the team invitation email addressed to to.
func InvitationMessage(to string, inv Invitation) (Message, error) {
	expires := inv.ExpiresAt.UTC().Format(expiryLayout)
	subject := fmt.Sprintf("%s invited you to %s on Sparkwing",
		singleLine(inv.InviterName), singleLine(inv.TeamName))

	text := fmt.Sprintf(`%s invited you to join the team %s on Sparkwing as %s.

Accept the invitation:
%s

This invitation expires on %s (7 days from when it was sent).

If you did not expect this invitation, you can ignore this email.
`, inv.InviterName, inv.TeamName, inv.Role, inv.AcceptURL, expires)

	var html bytes.Buffer
	if err := invitationHTML.Execute(&html, struct {
		InviterName, TeamName, Role, AcceptURL, Expires string
	}{inv.InviterName, inv.TeamName, inv.Role, inv.AcceptURL, expires}); err != nil {
		return Message{}, fmt.Errorf("mailer: render invitation HTML: %w", err)
	}

	return Message{To: to, Subject: subject, Text: text, HTML: html.String()}, nil
}

// singleLine keeps a crafted display name from injecting mail headers.
func singleLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == '\r' || r == '\n'
	}), " ")
}
