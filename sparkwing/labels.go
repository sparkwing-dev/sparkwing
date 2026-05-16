package sparkwing

import "strings"

// MatchLabels reports whether a runner advertising have can satisfy a
// label expression needed.
//
// Each entry in needed is one term. Within a term, comma-separated
// values are alternatives (OR). Across terms, results compose with
// AND. Empty terms and empty alternative values are ignored. Empty
// or nil needed matches any have (including empty).
//
//	MatchLabels(nil,                    any)              = true
//	MatchLabels([]string{"linux"},      ["linux"])        = true
//	MatchLabels([]string{"linux"},      ["macos"])        = false
//	MatchLabels([]string{"linux,macos"},["macos"])        = true   // OR within term
//	MatchLabels([]string{"linux","amd64"}, ["linux"])     = false  // AND across terms
//	MatchLabels([]string{"linux,macos","amd64"}, ["linux","amd64"]) = true
//
// Labels are equality strings; the matcher does no parsing of the
// key=value form -- "os=linux" is a single opaque string that must
// appear verbatim in have.
func MatchLabels(needed []string, have []string) bool {
	if len(needed) == 0 {
		return true
	}
	haveSet := make(map[string]struct{}, len(have))
	for _, l := range have {
		if l != "" {
			haveSet[l] = struct{}{}
		}
	}
	for _, term := range needed {
		if term == "" {
			continue
		}
		if !termSatisfied(term, haveSet) {
			return false
		}
	}
	return true
}

// MatchLabelsSet is MatchLabels with a pre-built advertising set.
// Useful when many candidates are checked against the same runner.
func MatchLabelsSet(needed []string, have map[string]struct{}) bool {
	if len(needed) == 0 {
		return true
	}
	for _, term := range needed {
		if term == "" {
			continue
		}
		if !termSatisfied(term, have) {
			return false
		}
	}
	return true
}

func termSatisfied(term string, have map[string]struct{}) bool {
	if !strings.ContainsRune(term, ',') {
		_, ok := have[strings.TrimSpace(term)]
		return ok
	}
	for _, alt := range strings.Split(term, ",") {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			continue
		}
		if _, ok := have[alt]; ok {
			return true
		}
	}
	return false
}
