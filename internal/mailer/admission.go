package mailer

import (
	"bytes"
	"fmt"
	"html/template"
)

const admissionSubject = "Your Sparkwing account is ready"

var admissionHTML = template.Must(template.New("admission").Parse(`<!DOCTYPE html>
<html lang="en">
<body style="margin:0;padding:24px;background:#ffffff;color:#1a1a1a;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;font-size:15px;line-height:1.5;">
<div style="max-width:560px;">
<p style="margin:0 0 16px;">You're off the Sparkwing waitlist, and your account and workspace are ready.</p>
<p style="margin:0 0 16px;"><a href="{{.}}" style="display:inline-block;padding:10px 16px;background:#1a1a1a;color:#ffffff;text-decoration:none;border-radius:4px;">Sign in</a></p>
<p style="margin:0 0 16px;">If the button does not work, open this link:<br><a href="{{.}}" style="color:#1a1a1a;word-break:break-all;">{{.}}</a></p>
<p style="margin:0;color:#555555;">If you did not sign up for Sparkwing, you can ignore this email.</p>
</div>
</body>
</html>
`))

// AdmissionMessage renders the email that tells a waitlisted person their
// account was admitted, with a link to sign in at signInURL.
func AdmissionMessage(to, signInURL string) (Message, error) {
	text := fmt.Sprintf(`You're off the Sparkwing waitlist, and your account and workspace are ready.

Sign in:
%s

If you did not sign up for Sparkwing, you can ignore this email.
`, signInURL)

	var html bytes.Buffer
	if err := admissionHTML.Execute(&html, signInURL); err != nil {
		return Message{}, fmt.Errorf("mailer: render admission HTML: %w", err)
	}
	return Message{To: to, Subject: admissionSubject, Text: text, HTML: html.String()}, nil
}
