package nodeproto

import "errors"

// ValidateAnnotation checks the host's annotation severity and source location.
func ValidateAnnotation(level string, line, endLine, column int) error {
	switch level {
	case "", "debug", "info", "warn", "error":
	default:
		return errors.New("invalid annotation level or location")
	}
	if line < 0 || endLine < 0 || column < 0 || (endLine > 0 && endLine < line) {
		return errors.New("invalid annotation level or location")
	}
	return nil
}
