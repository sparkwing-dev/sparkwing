package localhostwire

// LocalRunnerInstance identifies one process without changing its machine's placement identity.
type LocalRunnerInstance struct {
	ID               string `json:"id"`
	CapabilityPrefix string `json:"capability_prefix"`
	PID              int    `json:"pid"`
	ProcessBirth     string `json:"process_birth"`
	Version          string `json:"version"`
	BuildIdentity    string `json:"build_identity"`
	Machine          string `json:"machine"`
	State            string `json:"state"`
}
