package controllerwire

// AccountDeletionPreview is the scope displayed before confirming account deletion.
type AccountDeletionPreview struct {
	AccountID    string   `json:"account_id"`
	Teams        []string `json:"teams"`
	BlockedTeams []string `json:"blocked_teams"`
}

// AccountDeletionRequest repeats the account and every sole-member team being deleted.
type AccountDeletionRequest struct {
	AccountID string   `json:"confirm_account_id"`
	Teams     []string `json:"confirm_teams"`
}

// AccountDeletionResult identifies the account removed and teams queued for deletion.
type AccountDeletionResult struct {
	AccountID    string   `json:"account_id"`
	DeletedTeams []string `json:"deleted_teams"`
}
