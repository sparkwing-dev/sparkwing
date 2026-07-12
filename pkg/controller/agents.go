package controller

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// runnerHeadroomStale bounds how old an advertised-headroom report may be
// before the agents view drops it: a runner that stopped heartbeating is
// no longer a trustworthy source of free-capacity figures. It matches the
// 1h agents window so a runner that still shows active claims but stopped
// advertising simply loses its headroom line.
const runnerHeadroomStale = time.Hour

// Agent matches web/src/lib/api.ts:Agent. There's no explicit agent
// registration yet, so presence is inferred from recent node claims.
type Agent struct {
	Name          string            `json:"name"`
	Type          string            `json:"type"` // "agent" | "pool" | "local"
	Labels        map[string]string `json:"labels"`
	LastSeen      string            `json:"last_seen"`
	Status        string            `json:"status"` // "busy" | "idle"
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
}

// handleAgents returns agents inferred from the nodes table's
// claimed_by values over a 1h window. Idle agents without any recent
// claim activity don't appear.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	windowStart := time.Now().Add(-1 * time.Hour)

	rows, err := s.store.DB().QueryContext(r.Context(), `
SELECT run_id, node_id, status, claimed_by, COALESCE(started_at, 0), COALESCE(lease_expires_at, 0)
  FROM nodes
 WHERE claimed_by IS NOT NULL AND claimed_by != ''
   AND (lease_expires_at IS NOT NULL AND lease_expires_at >= ?)
`, windowStart.UnixNano())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = rows.Close() }()

	type holderInfo struct {
		holder, name, kind string
		lastSeenNs         int64
		activeRuns         map[string]struct{}
	}
	byHolder := map[string]*holderInfo{}

	for rows.Next() {
		var runID, nodeID, status, claimedBy string
		var startedNs, leaseExpNs int64
		if err := rows.Scan(&runID, &nodeID, &status, &claimedBy, &startedNs, &leaseExpNs); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		parts := strings.SplitN(claimedBy, ":", 3)
		if len(parts) < 2 {
			continue
		}
		kind := ""
		switch parts[0] {
		case "runner":
			kind = "agent"
		case "pod":
			kind = "pool"
		default:
			kind = parts[0]
		}
		name := parts[1]
		key := kind + ":" + name

		h, ok := byHolder[key]
		if !ok {
			h = &holderInfo{
				holder:     claimedBy,
				name:       name,
				kind:       kind,
				activeRuns: map[string]struct{}{},
			}
			byHolder[key] = h
		}
		h.lastSeenNs = max(h.lastSeenNs, startedNs, leaseExpNs)
		if status != "done" {
			h.activeRuns[runID] = struct{}{}
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
			Labels:        map[string]string{},
			LastSeen:      time.Unix(0, h.lastSeenNs).UTC().Format(time.RFC3339),
			Status:        status,
			ActiveJobs:    active,
			MaxConcurrent: 0,
		}
		if hr, ok := s.runnerHeadroom.lookup(h.name, time.Now(), runnerHeadroomStale); ok {
			agent.Headroom = &AgentHeadroom{
				Cores:       hr.Cores,
				MemoryBytes: hr.MemoryBytes,
				QueueDepth:  hr.QueueDepth,
			}
		}
		out = append(out, agent)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"agents": out})
}
