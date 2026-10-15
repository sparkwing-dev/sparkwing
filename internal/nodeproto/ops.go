package nodeproto

import (
	"fmt"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// Git is the trigger-time snapshot of the run's checkout.
type Git struct {
	SHA           string `json:"sha"`
	Branch        string `json:"branch"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Repo          string `json:"repo,omitempty"`
	RepoURL       string `json:"repo_url,omitempty"`
}

// Trigger describes what started the run: Source is manual, push, schedule
// or webhook.
type Trigger struct {
	Source      string       `json:"source"`
	User        string       `json:"user,omitempty"`
	PullRequest *PullRequest `json:"pull_request,omitempty"`
}

// PullRequest is the pull request a webhook run was triggered for.
type PullRequest struct {
	Number  int    `json:"number"`
	Action  string `json:"action,omitempty"`
	BaseRef string `json:"base_ref,omitempty"`
	BaseSHA string `json:"base_sha,omitempty"`
	HeadRef string `json:"head_ref,omitempty"`
	HeadSHA string `json:"head_sha,omitempty"`
}

// CheckOutputSize rejects output larger than the shared output bound.
func CheckOutputSize(n int) error {
	if int64(n) > limits.NodeOutputBytes {
		return fmt.Errorf("step output exceeds %d bytes", limits.NodeOutputBytes)
	}
	return nil
}
