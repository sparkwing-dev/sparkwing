package runspec

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxRetryReasonLength bounds the reason a retry records in its lineage.
const MaxRetryReasonLength = 1024

// ValidateRetryReason checks the reason a retry records. A retry that names an agent must give one
// (decision 94 labels runs agents start); a retry without an agent label may leave it empty. The text
// is shown in run views, so control characters such as newlines and escape sequences are refused.
func ValidateRetryReason(reason, agent string) error {
	if err := ValidateAgent(agent); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		if reason != "" {
			return fmt.Errorf("retry reason is blank; say why the retry should pass or omit the reason")
		}
		if agent != "" {
			return fmt.Errorf("agent %s retried without a reason; give --reason saying why the retry should pass, such as --reason %q or --reason %q (API: the retry request's reason field)", agent, "flake: DNS timeout in TestX", "fixed X in commit Y")
		}
		return nil
	}
	if len(reason) > MaxRetryReasonLength {
		return fmt.Errorf("retry reason is %d bytes; the limit is %d", len(reason), MaxRetryReasonLength)
	}
	if !utf8.ValidString(reason) {
		return fmt.Errorf("retry reason must be UTF-8 text")
	}
	if strings.IndexFunc(reason, unicode.IsControl) >= 0 {
		return fmt.Errorf("retry reason must be one line without control characters")
	}
	return nil
}
