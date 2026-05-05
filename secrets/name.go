package secrets

import (
	"fmt"
	"strings"
)

// nameCharset is the allowed alphabet for secret names. Letters,
// digits, and a small set of separators that keep names URL-safe,
// dotenv-safe, and friendly to namespacing schemes operators
// reach for (e.g. "github.token", "aws/prod/db_password").
const nameCharset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/-"

// ValidateName checks that a secret name is non-empty, within a
// sane length, and uses only the allowed charset. Returned errors
// are user-facing -- they get surfaced verbatim by the CLI and
// the controller handler.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("secret name is empty")
	}
	if len(name) > 256 {
		return fmt.Errorf("secret name too long (max 256, got %d)", len(name))
	}
	if i := strings.IndexFunc(name, func(r rune) bool {
		return !strings.ContainsRune(nameCharset, r)
	}); i >= 0 {
		return fmt.Errorf("secret name %q contains invalid character %q at index %d (allowed: A-Z a-z 0-9 . _ / -)", name, name[i:i+1], i)
	}
	if name[0] == '.' || name[0] == '/' || name[0] == '-' {
		return fmt.Errorf("secret name %q must not start with %q", name, name[0:1])
	}
	if last := name[len(name)-1]; last == '.' || last == '/' || last == '-' {
		return fmt.Errorf("secret name %q must not end with %q", name, name[len(name)-1:])
	}
	return nil
}
