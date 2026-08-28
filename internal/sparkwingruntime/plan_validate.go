package sparkwingruntime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func ValidateStepRange(p *sparkwing.Plan, startAt, stopAt string) error {
	if p == nil || (startAt == "" && stopAt == "") {
		return nil
	}
	known := planStepIDs(p)
	if startAt != "" {
		if _, ok := known[startAt]; !ok {
			return fmt.Errorf("%s", unknownRefMessage(
				fmt.Sprintf("--sw-start-at %q", startAt),
				"step",
				startAt,
				known,
			))
		}
	}
	if stopAt != "" {
		if _, ok := known[stopAt]; !ok {
			return fmt.Errorf("%s", unknownRefMessage(
				fmt.Sprintf("--sw-stop-at %q", stopAt),
				"step",
				stopAt,
				known,
			))
		}
	}
	return nil
}

func SuggestClosest(target string, candidates []string) string {
	return closestMatch(target, candidates)
}

func planStepIDs(p *sparkwing.Plan) map[string]struct{} {
	out := make(map[string]struct{})
	for _, n := range p.Nodes() {
		w := n.Work()
		if w == nil {
			continue
		}
		for k := range workKnownIDs(w) {
			out[k] = struct{}{}
		}
	}
	return out
}

func workKnownIDs(w *sparkwing.Work) map[string]struct{} {
	out := make(map[string]struct{})
	for _, s := range w.Steps() {
		out[s.ID()] = struct{}{}
	}
	for _, sp := range w.Spawns() {
		out[sp.ID()] = struct{}{}
	}
	for _, sg := range w.SpawnGens() {
		out[sg.ID()] = struct{}{}
	}
	return out
}

func unknownRefMessage(site, kind, missing string, known map[string]struct{}) string {
	available := sortedKeys(known)
	suggestion := closestMatch(missing, available)

	var b strings.Builder
	fmt.Fprintf(&b, "sparkwing: %s references unknown %s %q", site, kind, missing)
	if suggestion != "" {
		fmt.Fprintf(&b, "; did you mean %q?", suggestion)
	}
	if len(available) == 0 {
		fmt.Fprintf(&b, " (no %ss registered)", kind)
	} else {
		fmt.Fprintf(&b, " (available %ss: %s)", kind, strings.Join(available, ", "))
	}
	return b.String()
}

func closestMatch(want string, candidates []string) string {
	if want == "" || len(candidates) == 0 {
		return ""
	}
	bestDist := -1
	best := ""
	for _, c := range candidates {
		d := levenshtein(want, c)
		if bestDist < 0 || d < bestDist {
			bestDist = d
			best = c
		}
	}
	limit := max(2, longerLen(want, best)/3)
	if bestDist <= limit {
		return best
	}
	return ""
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	ar, br := []rune(a), []rune(b)
	la, lb := len(ar), len(br)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min3(
				curr[j-1]+1,
				prev[j]+1,
				prev[j-1]+cost,
			)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func longerLen(a, b string) int {
	if len(a) > len(b) {
		return len(a)
	}
	return len(b)
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
