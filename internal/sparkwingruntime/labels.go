package sparkwingruntime

import "strings"

func MatchLabels(needed, have []string) bool {
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
