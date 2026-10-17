package runspec

import "time"

// PublishedAttempt records completed local execution without an execution claim.
type PublishedAttempt struct {
	Attempt    int64     `json:"attempt"`
	Generation int64     `json:"generation"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Outcome    string    `json:"outcome"`
	Executor   string    `json:"executor"`
}
