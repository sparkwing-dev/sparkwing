package capability

import "time"

// OIDCFacts contains committed run identity and the live authority's issuance ceiling.
type OIDCFacts struct {
	Now          time.Time
	NotAfter     time.Time
	Team         string
	RunID        string
	Pipeline     string
	Trigger      string
	RunnerKind   string
	Ref          string
	SHA          string
	Repository   string
	RepositoryID int64
}
