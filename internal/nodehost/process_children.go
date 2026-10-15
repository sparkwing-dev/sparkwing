package nodehost

import (
	"time"
)

const (
	defaultAwait = 30 * time.Second
	maxAwait     = 60 * time.Second
	// safety: a child's args are a pipeline's inputs, not data, so a small
	// body bounds what a node can make the host hold.
	maxChildBody = 64 << 10
)

// ChildRequest starts a child pipeline and selects its eventual output job.
type ChildRequest struct {
	Pipeline string            `json:"pipeline"`
	Job      string            `json:"job,omitempty"`
	Args     map[string]string `json:"args,omitempty"`
	Repo     string            `json:"repo,omitempty"`
	Branch   string            `json:"branch,omitempty"`
}

func terminalRun(status string) bool {
	return status == "success" || status == "failed" || status == "cancelled"
}

func waitParam(raw string) (time.Duration, bool) {
	if raw == "" {
		return defaultAwait, true
	}
	wait, err := serviceDuration(raw)
	if err != nil || wait > maxAwait {
		return 0, false
	}
	return wait, true
}
