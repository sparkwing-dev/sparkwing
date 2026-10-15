package nodeproto

// MaskRequest registers a value with every output sink for the bound process.
// The host acknowledges only after registration. Values never enter logs, events or errors.
// A nonempty value matching an accepted identifier is refused by field path before acknowledgement.
// Successful registration also updates the matcher used for later generated declarations.
type MaskRequest struct {
	Value string `json:"value"`
}

// ServiceAcknowledged encodes {} after a bound node-host write has succeeded.
type ServiceAcknowledged struct{}

// AnnotationRequest preserves source location and optional step attribution.
// The host validates Step against the bound job, masks text, and applies limits.Record.
type AnnotationRequest struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	EndLine int    `json:"end_line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Step    string `json:"step,omitempty"`
}

// SummaryRequest replaces the bound job's or step's summary after masking and bounding.
type SummaryRequest struct {
	Markdown string `json:"markdown"`
	Step     string `json:"step,omitempty"`
}
