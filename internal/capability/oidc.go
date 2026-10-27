package capability

import "time"

// OIDCFacts contains committed run identity and the live authority's issuance ceiling.
type OIDCFacts struct {
	Now        time.Time
	NotAfter   time.Time
	Team       string
	RunID      string
	Pipeline   string
	Trigger    string
	RunnerKind string
	// RunnerEnvironment is fresh for a host no earlier job touched and
	// persistent for any other.
	RunnerEnvironment string
	Ref               string
	SHA               string
	Repository        string
	RepositoryID      int64
}
