package nodeproto

import (
	"fmt"
	"slices"
	"strings"
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

// Validate checks the annotation's severity and source location, naming the field at fault.
func (a Annotation) Validate() error {
	if a.Level != "" && !slices.Contains(AnnotationLevels, a.Level) {
		return fmt.Errorf("annotation level %q is not one of %s", a.Level, strings.Join(AnnotationLevels, ", "))
	}
	for _, field := range []struct {
		name  string
		value int
	}{{"line", a.Line}, {"end_line", a.EndLine}, {"column", a.Column}} {
		if field.value < 0 {
			return fmt.Errorf("annotation %s %d is negative", field.name, field.value)
		}
	}
	if a.EndLine > 0 && a.EndLine < a.Line {
		return fmt.Errorf("annotation end_line %d is before line %d", a.EndLine, a.Line)
	}
	return nil
}
