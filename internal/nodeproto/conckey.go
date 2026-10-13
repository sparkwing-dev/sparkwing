package nodeproto

import "strconv"

// Prefixes of a concurrency group's coordination key, one per scope.
const (
	ConcurrencyKeyGlobalPrefix = "g:"
	ConcurrencyKeyRunPrefix    = "r:"
	ConcurrencyKeyBoxPrefix    = "b:"
	// ConcurrencyKeyLenSep ends the qualifier's length in a run or box key.
	ConcurrencyKeyLenSep = ":"
)

// ConcurrencyGroupKey is the coordination key of the group name under scope
// ("run", "box", or global for anything else), the form [PlanConc.Key]
// carries. runID qualifies a run-scoped group and boxID, called only for a
// box-scoped one, a box-scoped group.
func ConcurrencyGroupKey(name, scope, runID string, boxID func() string) string {
	switch scope {
	case "run":
		return qualifiedConcurrencyKey(ConcurrencyKeyRunPrefix, runID, name)
	case "box":
		return qualifiedConcurrencyKey(ConcurrencyKeyBoxPrefix, boxID(), name)
	default:
		return ConcurrencyKeyGlobalPrefix + name
	}
}

// safety: the qualifier's length prefix keeps run "a" with group "bc" apart
// from run "ab" with group "c".
func qualifiedConcurrencyKey(prefix, qualifier, name string) string {
	return prefix + strconv.Itoa(len(qualifier)) + ConcurrencyKeyLenSep + qualifier + name
}
