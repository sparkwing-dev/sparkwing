package apierr

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// LastDiagnostic ignores earlier log records, which may describe unrelated failures.
func LastDiagnostic(output string) (*apierr.Error, error) {
	text := strings.TrimSpace(output)
	line := text[strings.LastIndexByte(text, '\n')+1:]
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		return nil, err
	}
	if envelope.Error == nil {
		return nil, errors.New("final record is not a CLI error envelope")
	}
	failure, err := apierr.DecodeInner(envelope.Error)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(Codes, func(d Definition) bool { return d.Code == failure.Code }) {
		return nil, errors.New("final CLI diagnostic has an unregistered error code")
	}
	return failure, nil
}
