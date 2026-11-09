package nodeproto

import (
	"bytes"
	"encoding/json"
	"fmt"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

// ValidateEvalValue checks the result against the kind declared by the accepted plan.
// Generated jobs still need ValidatePlan with the host's expansion context.
func ValidateEvalValue(r nodewire.EvalResult, kind string) error {
	bad := func() error { return fmt.Errorf("%w: invalid %s eval result", nodewire.ErrMalformed, kind) }
	if !planOneOf(kind, "skip_if", "memo_key", "before_run", "after_run", "verify", "generate") || !json.Valid(r.Value) {
		return bad()
	}
	raw := bytes.TrimSpace(r.Value)
	null := bytes.Equal(raw, []byte("null"))
	if r.Error != nil {
		if !null {
			return bad()
		}
		return nil
	}
	switch kind {
	case "skip_if":
		if !bytes.Equal(raw, []byte("true")) && !bytes.Equal(raw, []byte("false")) {
			return bad()
		}
	case "memo_key":
		if bytes.Equal(raw, []byte(`""`)) {
			return bad()
		}
	case "before_run", "after_run", "verify":
		if !null {
			return bad()
		}
	case "generate":
		var document struct {
			Jobs []json.RawMessage `json:"jobs"`
		}
		if null || json.Unmarshal(raw, &document) != nil || document.Jobs == nil {
			return bad()
		}
		for _, job := range document.Jobs {
			if len(bytes.TrimSpace(job)) == 0 || bytes.TrimSpace(job)[0] != '{' {
				return bad()
			}
		}
	}
	return nil
}
