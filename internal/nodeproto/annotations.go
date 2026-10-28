package nodeproto

import (
	"errors"
	"slices"
)

// AnnotationLevels are the severities an annotation may carry; an empty level reads as info.
var AnnotationLevels = []string{"debug", "info", "warn", "error"}

// Annotation is one annotation as the host accepts it, the controller stores it and the run views return it.
type Annotation struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	EndLine int    `json:"end_line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

// Validate checks the annotation's severity and source location.
func (a Annotation) Validate() error {
	if a.Level != "" && !slices.Contains(AnnotationLevels, a.Level) {
		return errors.New("invalid annotation level or location")
	}
	if a.Line < 0 || a.EndLine < 0 || a.Column < 0 || (a.EndLine > 0 && a.EndLine < a.Line) {
		return errors.New("invalid annotation level or location")
	}
	return nil
}
