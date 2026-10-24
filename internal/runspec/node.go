package runspec

// NodeRef identifies a run's job within a tenant-scoped request.
type NodeRef struct {
	RunID  RunID  `json:"run_id"`
	NodeID NodeID `json:"job_id"`
}
