package nodeproto

// Annotation is one annotation as the host accepts it, the controller stores it and the run views return it.
type Annotation struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	EndLine int    `json:"end_line,omitempty"`
	Column  int    `json:"column,omitempty"`
}
