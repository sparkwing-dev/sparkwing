package controllerwire

// NodeRef identifies a run's job within a tenant-scoped request.
type NodeRef struct {
	RunID  string `json:"run_id"`
	NodeID string `json:"job_id"`
}
