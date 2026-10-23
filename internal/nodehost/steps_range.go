package nodehost

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

// safety: a bound naming no step anywhere in the plan is a typo, and running
// the whole pipeline in its place could deploy what the author meant to skip.
func validateStepRange(doc nodeproto.PlanDoc, start, stop string) error {
	if start == "" && stop == "" {
		return nil
	}
	var known []string
	for _, n := range doc.Jobs {
		if len(n.Steps) > 0 {
			for _, st := range n.Steps {
				known = append(known, st.ID)
			}
		}
	}
	slices.Sort(known)
	known = slices.Compact(known)
	for _, bound := range [][2]string{{"--sw-start-at", start}, {"--sw-stop-at", stop}} {
		flag, id := bound[0], bound[1]
		if id == "" || slices.Contains(known, id) {
			continue
		}
		msg := fmt.Sprintf("sparkwing: %s %q references unknown step %q", flag, id, id)
		if s := sparkwingruntime.SuggestClosest(id, known); s != "" {
			msg += fmt.Sprintf("; did you mean %q?", s)
		}
		if len(known) == 0 {
			return fmt.Errorf("%s (no steps registered)", msg)
		}
		return fmt.Errorf("%s (available steps: %s)", msg, strings.Join(known, ", "))
	}
	return nil
}

// safety: a node holding neither bound runs every step, since the window may
// name another node's steps.
func rangeSkips(steps []nodeproto.StepSpec, children map[string][]string, start, stop string) map[string]string {
	parents := make(map[string][]string, len(steps))
	for _, st := range steps {
		parents[st.ID] = st.Needs
	}
	_, hasStart := parents[start]
	_, hasStop := parents[stop]
	hasStart, hasStop = hasStart && start != "", hasStop && stop != ""
	if !hasStart && !hasStop {
		return nil
	}
	var after, before map[string]bool
	if hasStart {
		after = reachable(start, children)
	}
	if hasStop {
		before = reachable(stop, parents)
	}
	reason := rangeReason(start, stop)
	skips := map[string]string{}
	for _, st := range steps {
		if (hasStart && !after[st.ID]) || (hasStop && !before[st.ID]) {
			skips[st.ID] = reason
		}
	}
	return skips
}

func reachable(from string, adj map[string][]string) map[string]bool {
	out := map[string]bool{}
	stack := []string{from}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !out[id] {
			out[id] = true
			stack = append(stack, adj[id]...)
		}
	}
	return out
}

func rangeReason(start, stop string) string {
	switch {
	case start != "" && stop != "":
		return fmt.Sprintf("outside --start-at=%s --stop-at=%s range", start, stop)
	case start != "":
		return "upstream of --start-at=" + start
	}
	return "downstream of --stop-at=" + stop
}
