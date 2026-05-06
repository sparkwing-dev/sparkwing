package sparkwing

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateStepRange resolves a --start-at / --stop-at pair against
// every Work materialized in p. Returns a non-nil error when a
// non-empty bound doesn't match any WorkStep / SpawnNode /
// SpawnNodeForEach id reachable from the Plan. The error message
// reuses the same `did you mean X?` formatting as IMP-008 so the
// operator sees one consistent class of typo error from every
// string-keyed flag (IMP-007).
//
// Empty bounds are no-ops (return nil). nil Plan returns nil.
func ValidateStepRange(p *Plan, startAt, stopAt string) error {
	if p == nil || (startAt == "" && stopAt == "") {
		return nil
	}
	known := planStepIDs(p)
	if startAt != "" {
		if _, ok := known[startAt]; !ok {
			return fmt.Errorf("%s", unknownRefMessage(
				fmt.Sprintf("--start-at %q", startAt),
				"step",
				startAt,
				known,
			))
		}
	}
	if stopAt != "" {
		if _, ok := known[stopAt]; !ok {
			return fmt.Errorf("%s", unknownRefMessage(
				fmt.Sprintf("--stop-at %q", stopAt),
				"step",
				stopAt,
				known,
			))
		}
	}
	return nil
}

// planStepIDs returns the set of every step / spawn / spawnGen id
// registered on every Work in p. The same registry IMP-008 walks
// for typo detection.
func planStepIDs(p *Plan) map[string]struct{} {
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

// validateRefs walks a finalized Plan and panics on any string-typed
// dependency reference that doesn't resolve to a registered ID. The
// pipeline runtime calls this once per Plan() invocation, after the
// user's Plan(ctx, plan, in, rc) returns nil, so authors discover
// typo'd Needs("...") strings at pipeline registration time -- not
// at first dispatch, when the typo would silently make the edge a
// no-op and the step would run immediately instead of after its
// intended predecessor (IMP-008).
//
// What gets validated:
//   - Plan-level: every Node's Needs string IDs must match a node
//     declared on the same Plan.
//   - Work-level: every WorkStep / SpawnHandle / SpawnGroup's Needs
//     string IDs must match a step or spawn registered on the same
//     Work.
//
// Out of scope (intentional): SkipPredicate closures (their bodies
// can't be introspected without ast walking; tracked in IMP-008's
// "out of scope" list).
func validateRefs(p *Plan) {
	if p == nil {
		return
	}
	validatePlanRefs(p)
	for _, n := range p.Nodes() {
		w := n.Work()
		if w == nil {
			continue
		}
		validateWorkRefs(n.ID(), w)
	}
}

// validatePlanRefs flags Node.Needs strings that don't resolve to a
// declared node on the same Plan. Handle-typed Needs(*Node) entries
// are guaranteed valid because the handle was returned by
// plan.Job() which registered it.
func validatePlanRefs(p *Plan) {
	known := make(map[string]struct{}, len(p.Nodes()))
	for _, n := range p.Nodes() {
		known[n.ID()] = struct{}{}
	}
	for _, n := range p.Nodes() {
		for _, depID := range n.DepIDs() {
			if _, ok := known[depID]; ok {
				continue
			}
			panic(unknownRefMessage(
				fmt.Sprintf("Node %q .Needs(%q)", n.ID(), depID),
				"plan node",
				depID,
				known,
			))
		}
	}
}

// validateWorkRefs flags WorkStep / Spawn deps that don't resolve to
// a step or spawn registered on the same Work. nodeID is the parent
// Plan node so the panic message can identify the offending
// pipeline section.
func validateWorkRefs(nodeID string, w *Work) {
	known := workKnownIDs(w)

	for _, s := range w.Steps() {
		for _, depID := range s.DepIDs() {
			if _, ok := known[depID]; ok {
				continue
			}
			panic(unknownRefMessage(
				fmt.Sprintf("node %q WorkStep %q .Needs(%q)", nodeID, s.ID(), depID),
				"step",
				depID,
				known,
			))
		}
	}
	for _, sp := range w.Spawns() {
		for _, depID := range sp.DepIDs() {
			if _, ok := known[depID]; ok {
				continue
			}
			panic(unknownRefMessage(
				fmt.Sprintf("node %q SpawnNode %q .Needs(%q)", nodeID, sp.ID(), depID),
				"step",
				depID,
				known,
			))
		}
	}
	for _, sg := range w.SpawnGens() {
		for _, depID := range sg.DepIDs() {
			if _, ok := known[depID]; ok {
				continue
			}
			panic(unknownRefMessage(
				fmt.Sprintf("node %q SpawnNodeForEach %q .Needs(%q)", nodeID, sg.ID(), depID),
				"step",
				depID,
				known,
			))
		}
	}
}

// workKnownIDs returns the set of valid dependency targets inside a
// Work: every step ID, every static SpawnNode ID, and every
// SpawnNodeForEach synthetic group ID.
func workKnownIDs(w *Work) map[string]struct{} {
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

// unknownRefMessage formats a uniform "did you mean X?" panic body
// for the validators above. kind is a short noun ("step", "plan
// node") used in the suggestion line; site describes the offending
// call site (e.g. `node "build" WorkStep "compile" .Needs("fetchh")`).
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

// SuggestClosest is the public projection of closestMatch for callers
// outside the sparkwing package (orchestrator main, cmd/sparkwing). It
// returns the candidate with the smallest Levenshtein distance to
// target, or "" if no candidate is close enough. Used by IMP-040 to
// share IMP-008's typo-suggestion threshold across "unknown pipeline"
// sites without duplicating the helper.
func SuggestClosest(target string, candidates []string) string {
	return closestMatch(target, candidates)
}

// closestMatch returns the candidate with the smallest Levenshtein
// distance to want, provided the distance is below a threshold
// proportional to want's length. Empty string when no candidate is
// "close enough" -- the panic message then falls back to listing
// every available ID so the operator can pick.
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
	// Threshold: at most 1/3 of the longer string, with a minimum of
	// 2. Calibrated so single-char typos in short ids ("fetch" vs
	// "fetchh") match while wholly different names ("apple" vs
	// "compile") don't.
	limit := max(2, longerLen(want, best)/3)
	if bestDist <= limit {
		return best
	}
	return ""
}

// levenshtein returns the edit distance between a and b. Two-row
// dynamic programming -- O(len(a)*len(b)) time, O(min) space.
// Inline to avoid pulling in a new module dep for ~25 lines.
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
				curr[j-1]+1,    // insertion
				prev[j]+1,      // deletion
				prev[j-1]+cost, // substitution
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
