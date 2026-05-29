package profile

import (
	"fmt"
	"maps"
	"os"
	"regexp"
	"strings"
)

// envVarPattern matches a single ${VAR} placeholder. The variable name
// must start with a letter or underscore and contain only letters,
// digits, and underscores -- POSIX shell-style identifier rules. We
// reject anything richer (e.g. ${VAR:-default}, $(cmd), nested braces)
// at the syntax level so a typo doesn't silently degrade into a shell
// expression that nobody can audit.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// rejectedShellLike catches richer shell-style constructs the
// interpolator does NOT support, so users get a clear error instead
// of confusing fallback behavior.
var rejectedShellLike = regexp.MustCompile(`\$\([^)]*\)|\$\{[A-Za-z_][A-Za-z0-9_]*[:?+-][^}]*\}|\$[A-Za-z_]`)

// ResolveDefaultArgs returns a copy of p.DefaultArgs with every
// ${VAR} placeholder replaced by os.Getenv("VAR"). Unset env vars
// expand to the empty string -- the resolution chain decides
// downstream whether "" is a valid value for the target arg type.
//
// Returns an error if any value contains shell-like syntax we
// deliberately don't support (POSIX ${VAR:-default}, $(cmd), bare
// $VAR without braces, etc.), naming the offending key so the
// operator can fix the YAML.
//
// Nil-safe: a nil profile returns (nil, nil).
func (p *Profile) ResolveDefaultArgs() (map[string]string, error) {
	if p == nil || len(p.DefaultArgs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(p.DefaultArgs))
	for key, raw := range p.DefaultArgs {
		// Reject unsupported syntax before doing any substitution; we
		// don't want to half-interpolate a value with both supported
		// and rejected fragments.
		if m := rejectedShellLike.FindString(raw); m != "" {
			return nil, fmt.Errorf(
				"profile %q default-args[%q]: unsupported shell-like syntax %q -- only ${VAR} env interpolation is supported",
				p.Name, key, m,
			)
		}
		expanded := envVarPattern.ReplaceAllStringFunc(raw, func(match string) string {
			// match is the full "${VAR}"; strip braces.
			name := match[2 : len(match)-1]
			return os.Getenv(name)
		})
		out[key] = expanded
	}
	return out, nil
}

// MergeDefaultArgs layers child's default-args on top of parent's
// (child wins on collision). Used at resolution time when multiple
// layers contribute defaults -- e.g. a builtin profile chained with
// a user override. Returns a new map; inputs are not mutated.
//
// Nil inputs are treated as empty; the result is non-nil iff either
// input was non-nil.
func MergeDefaultArgs(parent, child map[string]string) map[string]string {
	if parent == nil && child == nil {
		return nil
	}
	out := make(map[string]string, len(parent)+len(child))
	maps.Copy(out, parent)
	maps.Copy(out, child)
	return out
}

// DefaultArgsKeys returns the keys of a profile's default-args in
// sorted order. Useful for deterministic display in the describe-tree
// view and for stable error messages.
func (p *Profile) DefaultArgsKeys() []string {
	if p == nil || len(p.DefaultArgs) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.DefaultArgs))
	for k := range p.DefaultArgs {
		out = append(out, k)
	}
	// stable order
	sortStrings(out)
	return out
}

// sortStrings is a tiny helper that avoids pulling in sort just for
// one place. Keeps the package's imports minimal.
func sortStrings(s []string) {
	// Insertion sort; we expect <10 keys per profile in practice.
	for i := 1; i < len(s); i++ {
		j := i
		for j > 0 && strings.Compare(s[j-1], s[j]) > 0 {
			s[j-1], s[j] = s[j], s[j-1]
			j--
		}
	}
}
