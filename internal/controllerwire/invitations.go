package controllerwire

// InvitationCreate invites an address to the caller's team with a role no
// higher than the caller's own.
type InvitationCreate struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// InvitationCreated is a new or replayed invitation. AcceptURL works only for
// an account that signs in with the invited address. EmailSent is false when
// no email went out: the deployment has no mail service, the address or the
// inviting account reached its daily invitation email limit, or the send
// failed. EmailNote explains the inviting account's own limit and when it can
// send again; it is empty otherwise.
type InvitationCreated struct {
	ID        string `json:"id"`
	AcceptURL string `json:"accept_url"`
	EmailSent bool   `json:"email_sent"`
	EmailNote string `json:"email_note,omitempty"`
}
