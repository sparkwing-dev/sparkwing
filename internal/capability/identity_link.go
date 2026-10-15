package capability

import "time"

// IdentityLink binds a one-use provider exchange to its verified account session.
type IdentityLink struct {
	Authority AuthorityRef
	AccountID string
	Provider  string
	Nonce     string
	ExpiresAt time.Time
}
