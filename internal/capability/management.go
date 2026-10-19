package capability

import "time"

// Metadata is the public inventory projection of a controller credential.
type Metadata struct {
	Prefix         string     `json:"prefix"`
	Team           string     `json:"team"`
	Subject        Subject    `json:"subject"`
	Audience       Audience   `json:"audience"`
	Grants         []Grant    `json:"grants"`
	CreatedBy      string     `json:"created_by"`
	IssuedAt       time.Time  `json:"issued_at"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	GitCredentials bool       `json:"git_credentials"`
	Metered        bool       `json:"metered"`
	// Automation is a personal machine's standing for its owner's automated runs: none, requested or approved.
	Automation string `json:"automation,omitempty"`
}

// InventoryFilter selects metadata without changing its authenticated scope.
type InventoryFilter struct {
	SubjectKind    SubjectKind
	IncludeRevoked bool
}

// Rotation returns a committed replacement and the old credential's grace deadline.
type Rotation struct {
	Issued    Issued
	Previous  Metadata
	RevokesAt time.Time
}
