package runstate

import "time"

// RunSummary contains the fields exposed by the controller.
type RunSummary struct {
	RepoInherited   bool                `json:"repo_inherited,omitempty"`
	ID              string              `json:"id"`
	Number          int64               `json:"number"`
	Pipeline        string              `json:"pipeline"`
	Status          string              `json:"status"`
	TriggerSource   string              `json:"trigger_source,omitempty"`
	GitBranch       string              `json:"git_branch,omitempty"`
	GitSHA          string              `json:"git_sha,omitempty"`
	Args            map[string]string   `json:"args,omitempty"`
	Error           string              `json:"error,omitempty"`
	CreatedAt       time.Time           `json:"created_at,omitempty"`
	StartedAt       time.Time           `json:"started_at"`
	FinishedAt      *time.Time          `json:"finished_at,omitempty"`
	ParentRunID     string              `json:"parent_run_id,omitempty"`
	Admission       string              `json:"admission,omitempty"`
	DeclaredRepo    string              `json:"repo,omitempty"`
	RepoURL         string              `json:"repo_url,omitempty"`
	GithubOwner     string              `json:"github_owner,omitempty"`
	GithubRepo      string              `json:"github_repo,omitempty"`
	RetryOf         string              `json:"retry_of,omitempty"`
	RetriedAs       string              `json:"retried_as,omitempty"`
	RetrySource     string              `json:"retry_source,omitempty"`
	ReplayOfRunID   string              `json:"replay_of_run_id,omitempty"`
	ReplayOfNodeID  string              `json:"replay_of_node_id,omitempty"`
	Invocation      map[string]any      `json:"invocation,omitempty"`
	AnnotationCount int                 `json:"annotation_count,omitempty"`
	TopAnnotation   string              `json:"top_annotation,omitempty"`
	Annotations     []string            `json:"annotations,omitempty"`
	NeedsAttention  string              `json:"needs_attention,omitempty"`
	TriggerUser     string              `json:"trigger_user,omitempty"`
	PullRequest     *SummaryPullRequest `json:"pull_request,omitempty"`
}

// SummaryPullRequest contains the fields exposed by the controller.
type SummaryPullRequest struct {
	Number  int    `json:"number"`
	Action  string `json:"action,omitempty"`
	BaseRef string `json:"base_ref,omitempty"`
	BaseSHA string `json:"base_sha,omitempty"`
	HeadRef string `json:"head_ref,omitempty"`
	HeadSHA string `json:"head_sha,omitempty"`
}
