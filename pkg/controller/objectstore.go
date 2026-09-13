package controller

import (
	"fmt"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

// ObjectStoreBreakerResponse reports the process-wide object-store
// request budget, one entry per request class.
type ObjectStoreBreakerResponse struct {
	// Enabled is false when the process runs with the breaker switched
	// off, in which case requests are counted but never refused.
	Enabled bool `json:"enabled"`
	// Reset says what clears a tripped class without an operator:
	// "day" when the day window rolls, "manual" never.
	Reset string `json:"reset"`
	// Tripped is true while any class refuses requests.
	Tripped bool `json:"tripped"`
	// Classes carries the per-class counters and trip state.
	Classes []objectguard.ClassState `json:"classes"`
	// Cleared lists the classes an operator reset in this call.
	Cleared []string `json:"cleared,omitempty"`
}

func objectStoreBreakerState(l *objectguard.Limiter) ObjectStoreBreakerResponse {
	state := l.State()
	return ObjectStoreBreakerResponse{
		Enabled: state.Enabled,
		Reset:   string(state.Reset),
		Tripped: state.Tripped,
		Classes: state.Classes,
	}
}

func (s *Server) handleObjectStoreBreaker(w http.ResponseWriter, _ *http.Request) {
	limiter, err := objectguard.Shared()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, objectStoreBreakerState(limiter))
}

func (s *Server) handleResetObjectStoreBreaker(w http.ResponseWriter, _ *http.Request) {
	limiter, err := objectguard.Shared()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var cleared []string
	for _, c := range objectguard.Classes() {
		if limiter.ResetClass(c) {
			cleared = append(cleared, string(c))
		}
	}
	resp := objectStoreBreakerState(limiter)
	resp.Cleared = cleared
	writeJSON(w, http.StatusOK, resp)
}

func objectStoreHealth() (map[string]any, []string) {
	limiter, err := objectguard.Shared()
	if err != nil {
		return map[string]any{"tripped": false, "error": err.Error()},
			[]string{"object-store budget: " + err.Error()}
	}
	state := limiter.State()
	summary := map[string]any{"tripped": state.Tripped, "enabled": state.Enabled}
	var problems []string
	var tripped []string
	for _, c := range state.Classes {
		if !c.Tripped {
			continue
		}
		tripped = append(tripped, string(c.Class))
		problems = append(problems, fmt.Sprintf(
			"object-store %s budget: the per-%s limit of %d is spent and writes of this class fail closed",
			c.Class, c.TrippedWindow, trippedLimit(c)))
	}
	if len(tripped) > 0 {
		summary["tripped_classes"] = tripped
	}
	return summary, problems
}

func trippedLimit(c objectguard.ClassState) int {
	if c.TrippedWindow == objectguard.WindowDay {
		return c.PerDay
	}
	return c.PerMinute
}
