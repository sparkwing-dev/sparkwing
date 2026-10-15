package storage

import (
	"errors"
	"fmt"
)

// SafeSegment rejects empty identifiers, path references, separators and control characters in a single storage segment.
func SafeSegment(id string) error {
	if id == "" {
		return errors.New("storage: empty identifier")
	}
	if id == "." || id == ".." {
		return fmt.Errorf("storage: identifier %q is a path reference", id)
	}
	for _, r := range id {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return fmt.Errorf("storage: identifier %q contains a path separator or control character", id)
		}
	}
	return nil
}
