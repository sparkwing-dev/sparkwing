package secretname

import (
	"fmt"
	"strings"
)

const nameCharset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/-"

// Validate enforces the name grammar shared by secret storage and sealed
// execution policies.
func Validate(name string) error {
	if name == "" {
		return fmt.Errorf("secret name is empty")
	}
	if len(name) > 256 {
		return fmt.Errorf("secret name too long (max 256, got %d)", len(name))
	}
	if i := strings.IndexFunc(name, func(r rune) bool { return !strings.ContainsRune(nameCharset, r) }); i >= 0 {
		return fmt.Errorf("secret name %q contains invalid character %q at index %d (allowed: A-Z a-z 0-9 . _ / -)", name, name[i:i+1], i)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("secret name %q must not contain %q", name, "..")
	}
	if strings.Contains(name, "//") {
		return fmt.Errorf("secret name %q must not contain an empty segment", name)
	}
	if name[0] == '.' || name[0] == '/' || name[0] == '-' {
		return fmt.Errorf("secret name %q must not start with %q", name, name[0:1])
	}
	if last := name[len(name)-1]; last == '.' || last == '/' || last == '-' {
		return fmt.Errorf("secret name %q must not end with %q", name, name[len(name)-1:])
	}
	return nil
}
