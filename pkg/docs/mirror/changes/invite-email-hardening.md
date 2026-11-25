### Security

- **controller:** Keep user-chosen names out of the invitation email subject
  The subject is now always "You're invited to a team on Sparkwing". The inviter's name and the team name appear only in the body, quoted as names Sparkwing users chose, so they cannot read as Sparkwing's own words in an authenticated message. See [capability inventory and rotation](docs/auth.md#capability-inventory-and-rotation).
- **controller:** Accept only a plain email address for a team invitation
  An address with a display name or angle brackets, such as `"text"<name@example.com>`, is refused with `400` and a request for a plain address. Each such spelling of one mailbox had its own five-per-day invitation email allowance.
