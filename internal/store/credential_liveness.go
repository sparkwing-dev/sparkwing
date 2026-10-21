package store

// liveCredentialSQL takes two identical store-clock arguments: grace, then expiry.
func liveCredentialSQL(alias string) string {
	return "(" + alias + "revoked_at IS NULL OR " + alias + "revoked_at>?) AND (" + alias + "expires_at IS NULL OR " + alias + "expires_at>?)"
}
