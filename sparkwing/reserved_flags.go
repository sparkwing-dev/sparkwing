package sparkwing

import (
	"fmt"
	"sort"
	"strings"
)

// reservedFlagNames is the canonical list of wing-owned long flag names
// that the cmd/sparkwing wing-flag parser consumes before any pipeline
// flag parsing happens. A pipeline Args struct that declares one of
// these as a `flag:"..."` tag would silently lose the value to wing
// (the underlying IMP-003 collision bug), so Register panics at
// registration time when a collision is detected.
//
// This list is the single source of truth; cmd/sparkwing/wing_flags.go
// imports it via ReservedFlagNames() so the two surfaces can never
// drift. When a new wing-owned flag is added in wing_flags.go, append
// it here so authors get a useful panic instead of a silent collision.
//
// Stored without the leading "--" since pipeline `flag:"..."` tags use
// the bare name. Short aliases (-v, -C) are tracked alongside their
// long forms; an Args struct's `flag:"v"` would not collide with
// `wing -v` at the parser level (parser only reads short forms with a
// single dash, not as flag-tag names), but reserving them anyway
// keeps the contract symmetric and forward-compatible if the parser
// ever grows long-style short handling.
var reservedFlagNames = []string{
	"from",
	"on",
	"config",
	"retry-of",
	"full",
	"no-update",
	"verbose",
	"v",
	"secrets",
	"mode",
	"workers",
	"change-directory",
	"C",
	"start-at",
	"stop-at",
}

// ReservedFlagNames returns the wing-owned flag names a pipeline Args
// struct is forbidden from declaring as a `flag:"..."` tag. The
// returned slice is a sorted copy; callers may mutate it freely.
//
// Documented use: scaffolders / linters that want to reject a
// generated Args struct before it reaches Register, panic-message
// formatters that want to print "(reserved: X, Y, Z)", and tests
// that pin the contract.
func ReservedFlagNames() []string {
	out := make([]string, len(reservedFlagNames))
	copy(out, reservedFlagNames)
	sort.Strings(out)
	return out
}

// isReservedFlagName reports whether name (without leading "--") is
// in the wing-owned reserved set. Used by Register's collision check.
func isReservedFlagName(name string) bool {
	for _, r := range reservedFlagNames {
		if r == name {
			return true
		}
	}
	return false
}

// validateReservedFlagCollisions panics if any field in schema declares
// a wing-reserved flag name as its `flag:"..."` tag. The panic message
// names the pipeline, the offending Go field, the colliding flag, and
// the full reserved list so the author can rename without re-discovering
// the contract elsewhere.
func validateReservedFlagCollisions(pipelineName string, schema InputSchema) {
	for _, f := range schema.Fields {
		if f.isExtraBag {
			continue
		}
		if isReservedFlagName(f.Name) {
			reserved := ReservedFlagNames()
			panic(fmt.Sprintf(
				"sparkwing.Register(%q): pipeline flag --%s on field %s collides with the reserved wing flag --%s; rename it (e.g. --my-%s) or remove the tag. Reserved wing flags: %s",
				pipelineName,
				f.Name,
				f.GoName,
				f.Name,
				f.Name,
				strings.Join(reserved, ", "),
			))
		}
	}
}
