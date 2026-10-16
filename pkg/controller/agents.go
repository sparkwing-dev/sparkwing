package controller

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const runnerHeadroomStale = time.Hour

// Agent is a public runner observation without credential or holder identifiers.
type Agent struct {
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	Location      string            `json:"location"`
	Labels        map[string]string `json:"labels"`
	Capabilities  []string          `json:"capabilities,omitempty"`
	LastSeen      string            `json:"last_seen"`
	Status        string            `json:"status"`
	ActiveJobs    []string          `json:"active_jobs"`
	MaxConcurrent int               `json:"max_concurrent"`
	Headroom      *AgentHeadroom    `json:"headroom,omitempty"`
}

// AgentHeadroom is free capacity measured at the store's observation time.
type AgentHeadroom struct {
	Cores       float64 `json:"cores"`
	MemoryBytes int64   `json:"memory_bytes"`
	QueueDepth  int     `json:"queue_depth"`
	ObservedAt  string  `json:"observed_at"`
}

// RunnerInventoryReader projects tenant runners without inferring identity from holders.
type RunnerInventoryReader interface {
	Team() store.Team
	ReadRunners(context.Context) (capability.RunnerSnapshot, error)
}

var _ RunnerInventoryReader = (*store.Tenant)(nil)

// WithRunnerInventory supplies tenant snapshots before Handler is constructed.
func (s *Server) WithRunnerInventory(resolve func(context.Context, store.Team) (RunnerInventoryReader, error)) *Server {
	s.resolveRunnerInventory = resolve
	return s
}

func (s *Server) runnerInventory(ctx context.Context, team string) (capability.RunnerSnapshot, error) {
	if team == "" {
		return capability.RunnerSnapshot{}, missingEndpointPolicy()
	}
	var reader RunnerInventoryReader
	var err error
	if s.resolveRunnerInventory != nil {
		reader, err = s.resolveRunnerInventory(ctx, store.Team(team))
	} else if s.store != nil {
		var tenant *store.Tenant
		tenant, err = s.store.ForTeam(ctx, store.Team(team))
		reader, _ = any(tenant).(RunnerInventoryReader)
	}
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		return capability.RunnerSnapshot{}, engineLookupError(store.ErrNotFound)
	}
	if err != nil {
		return capability.RunnerSnapshot{}, engineLookupError(err)
	}
	if reader == nil || reader.Team() != store.Team(team) {
		return capability.RunnerSnapshot{}, missingEndpointPolicy()
	}
	snapshot, err := reader.ReadRunners(ctx)
	if err != nil {
		return capability.RunnerSnapshot{}, engineLookupError(err)
	}
	if snapshot.Now.IsZero() {
		return capability.RunnerSnapshot{}, missingEndpointPolicy()
	}
	for _, row := range snapshot.Runners {
		if row.Team != team || row.Runner.Name == "" || row.Runner.Type == "" || row.MaxConcurrent < 0 || row.LastSeen.After(snapshot.Now) {
			return capability.RunnerSnapshot{}, missingEndpointPolicy()
		}
		if h := row.Headroom; h != nil && (math.IsNaN(h.Cores) || math.IsInf(h.Cores, 0) || h.Cores < 0 || h.MemoryBytes < 0 || h.QueueDepth < 0 || h.ObservedAt.IsZero() || h.ObservedAt.After(snapshot.Now)) {
			return capability.RunnerSnapshot{}, missingEndpointPolicy()
		}
	}
	return snapshot, nil
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	snapshot, err := s.runnerInventory(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	out := make([]Agent, 0, len(snapshot.Runners))
	for _, row := range snapshot.Runners {
		active := append([]string{}, row.ActiveJobs...)
		slices.Sort(active)
		agent := Agent{
			Name: row.Runner.Name, Type: row.Runner.Type, Location: row.Location,
			Labels: map[string]string{}, Capabilities: slices.Clone(row.Runner.Labels), ActiveJobs: slices.Compact(active),
			Status: "idle", MaxConcurrent: row.MaxConcurrent,
		}
		if !row.LastSeen.IsZero() {
			agent.LastSeen = row.LastSeen.UTC().Format(time.RFC3339Nano)
		}
		switch {
		case len(active) > 0:
			agent.Status = "busy"
		case row.LastSeen.IsZero() || snapshot.Now.Sub(row.LastSeen) > agentOnlineWithin:
			agent.Status = "offline"
		}
		if h := row.Headroom; h != nil && snapshot.Now.Sub(h.ObservedAt) <= runnerHeadroomStale {
			agent.Headroom = &AgentHeadroom{Cores: h.Cores, MemoryBytes: h.MemoryBytes, QueueDepth: h.QueueDepth, ObservedAt: h.ObservedAt.UTC().Format(time.RFC3339Nano)}
		}
		out = append(out, agent)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
	return nil
}

func (s *Server) sampleRunners(ctx context.Context) {
	teams, err := s.store.AsOperator().ListTeams(ctx)
	if err != nil {
		s.logger.Error("runner sample failed", "err", err)
		return
	}
	var labels [][]string
	for _, team := range teams {
		snapshot, err := s.runnerInventory(ctx, string(team))
		if err != nil {
			s.logger.Error("runner sample failed", "err", err)
			return
		}
		for _, row := range snapshot.Runners {
			if !row.LastSeen.IsZero() && snapshot.Now.Sub(row.LastSeen) <= runnerLivenessWindow {
				labels = append(labels, row.Runner.Labels)
			}
		}
	}
	setActiveRunners(len(labels))
	liveRunners.sample(labels)
}
