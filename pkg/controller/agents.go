package controller

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

const runnerHeadroomStale = time.Hour

type Agent struct {
	Name          string            `json:"name"`
	Type          string            `json:"type"` // "agent" | "pool" | "local"
	Location      string            `json:"location"`
	Labels        map[string]string `json:"labels"`
	Capabilities  []string          `json:"capabilities,omitempty"`
	LastSeen      string            `json:"last_seen"`
	Status        string            `json:"status"` // "busy" | "idle" | "offline"
	ActiveJobs    []string          `json:"active_jobs"`
	MaxConcurrent int               `json:"max_concurrent"`
	// Headroom is the runner's most recently advertised free capacity --
	// the local admission daemon's grantable cores/memory after the
	// operator's reserve, plus the daemon's queue depth. Nil for a runner
	// that never advertised (it engages no local daemon, or predates the
	// headroom protocol), or when its last report has gone stale.
	Headroom *AgentHeadroom `json:"headroom,omitempty"`
}

// AgentHeadroom is a runner's advertised free capacity in the agents view.
type AgentHeadroom struct {
	Cores       float64 `json:"cores"`
	MemoryBytes int64   `json:"memory_bytes"`
	QueueDepth  int     `json:"queue_depth"`
	// ObservedAt is when the controller accepted this measurement, not a
	// worker-supplied clock value.
	ObservedAt string `json:"observed_at"`
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	windowStart := time.Now().Add(-1 * time.Hour)
	claims, err := tenant.ListAgentClaims(r.Context(), windowStart)
	if err != nil {
		return engineLookupError(err)
	}

	type holderInfo struct {
		name, kind, tokenPrefix string
		lastSeenNs              int64
		activeRuns              map[string]struct{}
	}
	byHolder := map[string]*holderInfo{}
	sort.Slice(claims, func(i, j int) bool {
		if !claims[i].StartedAt.Equal(claims[j].StartedAt) {
			return claims[i].StartedAt.After(claims[j].StartedAt)
		}
		if claims[i].TokenPrefix != claims[j].TokenPrefix {
			return claims[i].TokenPrefix < claims[j].TokenPrefix
		}
		return claims[i].RunID < claims[j].RunID
	})

	for _, claim := range claims {
		name, kind := holderName(claim.ClaimedBy)
		if name == "" {
			continue
		}
		key := kind + ":" + name

		h, ok := byHolder[key]
		if !ok {
			lastSeenNs := int64(0)
			if !claim.LastSeen.IsZero() {
				lastSeenNs = claim.LastSeen.UnixNano()
			}
			h = &holderInfo{
				name:        name,
				kind:        kind,
				tokenPrefix: claim.TokenPrefix,
				lastSeenNs:  lastSeenNs,
				activeRuns:  map[string]struct{}{},
			}
			byHolder[key] = h
		}
		if claim.TokenPrefix != h.tokenPrefix {
			continue
		}
		if !claim.LastSeen.IsZero() {
			if seen := claim.LastSeen.UnixNano(); seen > h.lastSeenNs {
				h.lastSeenNs = seen
			}
		}
		if claim.Status != "done" {
			h.activeRuns[claim.RunID] = struct{}{}
		}
	}

	out := make([]Agent, 0, len(byHolder))
	for _, h := range byHolder {
		status := "idle"
		if len(h.activeRuns) > 0 {
			status = "busy"
		}
		active := make([]string, 0, len(h.activeRuns))
		for r := range h.activeRuns {
			active = append(active, r)
		}
		agent := Agent{
			Name:          h.name,
			Type:          h.kind,
			Location:      "unknown",
			Labels:        map[string]string{},
			Status:        status,
			ActiveJobs:    active,
			MaxConcurrent: 0,
		}
		if h.lastSeenNs > 0 {
			agent.LastSeen = time.Unix(0, h.lastSeenNs).UTC().Format(time.RFC3339)
		}
		if presence, ok := s.runnerPresence.lookup(presenceKey{tokenPrefix: h.tokenPrefix, name: h.name}, time.Now(), runnerHeadroomStale); ok {
			agent.Capabilities = presence.Labels
			agent.MaxConcurrent = presence.MaxConcurrent
			if presence.UpdatedAt.UnixNano() > h.lastSeenNs {
				h.lastSeenNs = presence.UpdatedAt.UnixNano()
				agent.LastSeen = presence.UpdatedAt.UTC().Format(time.RFC3339)
			}
		}
		if observed, ok := s.runnerHeartbeats.lookup(presenceKey{tokenPrefix: h.tokenPrefix, name: h.name}, time.Now()); ok && observed.UnixNano() > h.lastSeenNs {
			agent.LastSeen = observed.UTC().Format(time.RFC3339)
		}
		if hr, ok := s.runnerHeadroom.lookup(presenceKey{tokenPrefix: h.tokenPrefix, name: h.name}, time.Now(), runnerHeadroomStale); ok {
			agent.Headroom = &AgentHeadroom{
				Cores:       hr.Cores,
				MemoryBytes: hr.MemoryBytes,
				QueueDepth:  hr.QueueDepth,
				ObservedAt:  hr.UpdatedAt.UTC().Format(time.RFC3339),
			}
		}
		out = append(out, agent)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"agents": out})
	return nil
}
