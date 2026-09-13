package controller

import (
	"fmt"
	"net/http"
	"time"

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
	resp := objectStoreBreakerState(limiter)
	for _, c := range limiter.Reset() {
		resp.Cleared = append(resp.Cleared, string(c))
	}
	writeJSON(w, http.StatusOK, resp)
}

// safety: the health route answers without a token, so the summary says which
// classes refuse and never what their limits are.
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
			"object-store %s budget: the per-%s limit is spent and requests of this class are refused",
			c.Class, c.TrippedWindow))
	}
	if len(tripped) > 0 {
		summary["tripped_classes"] = tripped
	}

	stalls := objectguard.Stalls()
	if len(stalls) > 0 {
		summary["stalled"] = stalls
	}
	for _, st := range stalls {
		problems = append(problems, fmt.Sprintf(
			"object-store replay stalled since %s: %s", st.Since.Format(time.RFC3339), st.Path))
	}
	return summary, problems
}
