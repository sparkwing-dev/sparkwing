package wingd

import "github.com/sparkwing-dev/sparkwing/pkg/wingwire"

// AdmissionHTTPRequest identifies one retryable machine admission operation.
type AdmissionHTTPRequest struct {
	RequestKey   string                    `json:"request_key"`
	Request      wingwire.AdmissionRequest `json:"request"`
	RootPID      int                       `json:"root_pid"`
	ProcessBirth string                    `json:"process_birth"`
}

// AdmissionHTTPState is a durable ticket, grant or terminal admission outcome.
type AdmissionHTTPState struct {
	Revision uint64            `json:"revision"`
	ID       string            `json:"id"`
	State    string            `json:"state"`
	Arrival  uint64            `json:"arrival"`
	Grant    *wingwire.Grant   `json:"grant,omitempty"`
	Queued   *wingwire.Queued  `json:"queued,omitempty"`
	Evicted  *wingwire.Evicted `json:"evicted,omitempty"`
	Cancel   *wingwire.Cancel  `json:"cancel,omitempty"`
}
