package nodehost

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// safety: a failed command's error embeds its whole output, which the node's
// log already holds, and the controller refuses a finish body over 1 MiB, so
// the row and end event carry a bounded error.
const (
	nodeErrTextMaxBytes  = 8 << 10
	nodeErrTextHeadBytes = 512
)

// safety: only the text is masked, so errors.Is and As still reach the cause.
type maskedRunError struct {
	err       error
	text      string
	canonical *apierr.Error
}

func (e maskedRunError) Error() string { return e.text }
func (e maskedRunError) Unwrap() error { return e.err }

func (e maskedRunError) As(target any) bool {
	canonical, ok := target.(**apierr.Error)
	if !ok || e.canonical == nil {
		return false
	}
	*canonical = e.canonical
	return true
}

func maskHostError(err error, mask func(string) string) error {
	if err == nil {
		return nil
	}
	masked := maskedRunError{err: err, text: boundedNodeErrText(mask(err.Error()))}
	var canonical *apierr.Error
	if errors.As(err, &canonical) {
		masked.canonical = &apierr.Error{Code: maskedFailureCode(canonical.Code, mask), Message: masked.text, Details: secrets.MaskDetails(canonical.Details, mask)}
		if canonical.Hint != "" {
			masked.canonical.Hint = boundedNodeErrText(mask(canonical.Hint))
		}
	}
	return masked
}

// safety: the first line names what failed and the tail is where a command's
// output reports why, so both survive and the elision is stated.
func boundedNodeErrText(text string) string {
	if len(text) <= nodeErrTextMaxBytes {
		return text
	}
	head, _, _ := strings.Cut(text, "\n")
	if len(head) > nodeErrTextHeadBytes {
		head = strings.ToValidUTF8(head[:nodeErrTextHeadBytes], "")
	}
	tail := text[len(text)-(nodeErrTextMaxBytes-nodeErrTextHeadBytes-128):]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	tail = strings.ToValidUTF8(tail, "")
	elided := len(text) - len(head) - len(tail)
	return fmt.Sprintf("%s\n… (%d bytes of this error elided)\n%s", head, elided, tail)
}
