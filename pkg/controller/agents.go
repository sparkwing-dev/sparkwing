package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const runnerHeadroomStale = time.Hour

// Agent matches web/src/lib/api.ts:Agent. Location is display-only.
type Agent struct {
	Name            string            `json:"name"`
	Type            string            `json:"type"` // "agent" | "gateway" | "pool" | "local"
	Location        string            `json:"location"`
	Labels          map[string]string `json:"labels"`
	Capabilities    []string          `json:"capabilities,omitempty"`
	LastSeen        string            `json:"last_seen"`
	Status          string            `json:"status"` // "busy" | "idle" | "offline"
	ActiveJobs      []string          `json:"active_jobs"`
	ActiveSlots     *int              `json:"active_slots,omitempty"`
	MaxConcurrent   int               `json:"max_concurrent"`
	BasePriority    int               `json:"base_priority"`
	PriorityCeiling int               `json:"priority_ceiling"`
	Budget          AgentResources    `json:"budget"`
	// Headroom is the runner's most recently advertised free capacity --
	// the local admission daemon's grantable cores/memory after the
	// operator's reserve, plus the daemon's queue depth. Nil for a runner
	// that never advertised (it engages no local daemon, or predates the
	// headroom protocol), or when its last report has gone stale.
	Headroom *AgentHeadroom `json:"headroom,omitempty"`
}

// AgentResources is an executor's configured contribution ceiling.
type AgentResources struct {
	Cores       float64 `json:"cores"`
	MemoryBytes int64   `json:"memory_bytes"`
}

// AgentHeadroom is a runner's advertised free capacity in the agents view.
type AgentHeadroom struct {
	Cores       float64 `json:"cores"`
	MemoryBytes int64   `json:"memory_bytes"`
	QueueDepth  int     `json:"queue_depth"`
}

type enrollAgentReq struct {
	TokenPrefix     string         `json:"token_prefix"`
	Kind            string         `json:"kind"`
	Location        string         `json:"location"`
	Capabilities    []string       `json:"capabilities,omitempty"`
	BasePriority    int            `json:"base_priority"`
	PriorityCeiling int            `json:"priority_ceiling"`
	MaxConcurrent   int            `json:"max_concurrent"`
	Budget          AgentResources `json:"budget"`
}

func normalizeEnrollment(name string, in enrollAgentReq) (store.Executor, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, ":") {
		return store.Executor{}, errors.New("executor name is required and cannot contain ':'")
	}
	switch in.Kind {
	case "agent", "gateway":
	default:
		return store.Executor{}, fmt.Errorf("executor kind %q: expected agent or gateway", in.Kind)
	}
	if in.Location == "" {
		in.Location = "unknown"
	}
	switch in.Location {
	case "local", "cloud", "unknown":
	default:
		return store.Executor{}, fmt.Errorf("executor location %q: expected local, cloud, or unknown", in.Location)
	}
	if in.BasePriority < 0 || in.BasePriority > 100 || in.PriorityCeiling < in.BasePriority || in.PriorityCeiling > 100 {
		return store.Executor{}, errors.New("executor priority must satisfy 0 <= base_priority <= priority_ceiling <= 100")
	}
	if in.MaxConcurrent < 1 || in.Budget.Cores < 0 || math.IsNaN(in.Budget.Cores) || math.IsInf(in.Budget.Cores, 0) || in.Budget.MemoryBytes < 0 {
		return store.Executor{}, errors.New("executor limits must be finite and non-negative with max_concurrent >= 1")
	}
	seen := map[string]bool{}
	capabilities := make([]string, 0, len(in.Capabilities))
	for _, capability := range in.Capabilities {
		capability = strings.TrimSpace(capability)
		if capability != "" && !seen[capability] {
			seen[capability] = true
			capabilities = append(capabilities, capability)
		}
	}
	sort.Strings(capabilities)
	return store.Executor{
		Name: name, Kind: in.Kind, Location: in.Location, Capabilities: capabilities,
		BasePriority: in.BasePriority, PriorityCeiling: in.PriorityCeiling,
		MaxConcurrent: in.MaxConcurrent,
		Budget:        store.ExecutorResource{Cores: in.Budget.Cores, MemoryBytes: in.Budget.MemoryBytes},
	}, nil
}

func (s *Server) handleEnrollAgent(w http.ResponseWriter, r *http.Request) {
	var body enrollAgentReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	executor, err := normalizeEnrollment(r.PathValue("name"), body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	now := time.Now()
	token, err := s.store.LookupTokenByPrefix(strings.TrimSpace(body.TokenPrefix))
	if err != nil || !token.IsValid(now) ||
		(token.Kind != store.TokenKindRunner && token.Kind != store.TokenKindService) || !token.HasScope(ScopeNodesClaim) {
		writeError(w, http.StatusBadRequest, errors.New("token_prefix must name a live runner or service token with nodes.claim"))
		return
	}
	executor.Principal = token.Principal
	if err := s.store.EnrollExecutor(r.Context(), token.Prefix, executor); err != nil {
		// safety: PostgreSQL constraint errors can expose credential prefixes
		writeError(w, http.StatusConflict, errors.New("executor enrollment conflicts with an existing name or credential"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type agentHeartbeatReq struct {
	Headroom *claimHeadroom `json:"headroom"`
}

func validateClaimHeadroom(headroom *claimHeadroom) error {
	if headroom == nil || headroom.Cores < 0 || math.IsNaN(headroom.Cores) || math.IsInf(headroom.Cores, 0) ||
		headroom.MemoryBytes < 0 || headroom.QueueDepth < 0 {
		return errors.New("headroom must contain finite non-negative cores, memory_bytes, and queue_depth")
	}
	return nil
}

func (s *Server) handleHeartbeatAgent(w http.ResponseWriter, r *http.Request) {
	var body agentHeartbeatReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validateClaimHeadroom(body.Headroom); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.HeartbeatExecutor(r.Context(), claimIdentity(r), r.PathValue("name"),
		store.ExecutorResource{Cores: body.Headroom.Cores, MemoryBytes: body.Headroom.MemoryBytes},
		body.Headroom.QueueDepth, time.Now()); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	registered, err := s.registeredAgents(r.Context(), time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	windowStart := time.Now().Add(-1 * time.Hour)

	rows, err := s.store.DB().QueryContext(r.Context(), `
SELECT run_id, node_id, status, claimed_by, COALESCE(started_at, 0), COALESCE(lease_expires_at, 0)
  FROM nodes
	 WHERE claimed_by IS NOT NULL AND claimed_by != ''
	   AND claim_executor = ''
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

	out := append([]Agent(nil), registered...)
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
