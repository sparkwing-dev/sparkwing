package apierr

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// LastDiagnostic ignores earlier log records, which may describe unrelated failures.
func LastDiagnostic(output string) *apierr.Error {
	text := strings.TrimSpace(output)
	line := text[strings.LastIndexByte(text, '\n')+1:]
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(line), &envelope) != nil || envelope.Error == nil {
		return nil
	}
	failure, err := apierr.DecodeInner(envelope.Error)
	if err != nil || !slices.ContainsFunc(Codes, func(d Definition) bool { return d.Code == failure.Code }) {
		return nil
	}
	return failure
}
