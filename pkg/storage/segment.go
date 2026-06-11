package storage

import (
	"errors"
	"fmt"
)

// SafeSegment validates an identifier (run ID, node ID) used as a
// single path or object-key segment: non-empty, not a path reference
// ("." or ".."), and free of path separators and control characters.
// Storage backends reject unsafe identifiers at the boundary, so an
// identifier arriving over HTTP can never escape its run directory on
// the filesystem backend or corrupt a key listing on the object-store
// backend. The SDK enforces the same hygiene on node IDs at plan
// time; this is the authoritative check.
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

// SafeSegments validates several identifiers at once, returning the
// first violation.
func SafeSegments(ids ...string) error {
	for _, id := range ids {
		if err := SafeSegment(id); err != nil {
			return err
		}
	}
	return nil
}
