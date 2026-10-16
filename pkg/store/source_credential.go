package store

// SourceSpec is what a pipeline's plan asks of its checkout. A plan claim, and
// a plan that declares nothing, gets one commit and nothing else.
type SourceSpec struct {
	Depth      int  `json:"depth"`
	Tags       bool `json:"tags"`
	Submodules bool `json:"submodules"`
	LFS        bool `json:"lfs"`
}

// SourceCredential is the one credential a claim fetches its run's source
// with, and the commit and checkout the run asks for. Kind "github_app" is a
// GitHub App token in Token that reads exactly Repositories; "https" and
// "ssh" are the team's stored credential for Host, in Username and Secret,
// with KnownHosts pinning an ssh host key.
type SourceCredential struct {
	Kind      string `json:"kind"`
	Host      string `json:"host"`
	Token     string `json:"token,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	RepoURL   string `json:"repo_url,omitempty"`
	SHA       string `json:"sha,omitempty"`
	Branch    string `json:"branch,omitempty"`
	// Repositories are owner/name, the run's own first, then the ones a team
	// owner listed for it; empty for a run on a host other than github.com.
	Repositories []string   `json:"repositories"`
	Source       SourceSpec `json:"source"`
	Username     string     `json:"username,omitempty"`
	Secret       string     `json:"secret,omitempty"`
	KnownHosts   string     `json:"known_hosts,omitempty"`
}
