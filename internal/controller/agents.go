package controller

import (
	"context"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const runnerHeadroomStale = time.Hour

// runnerInventoryReader projects tenant runners without inferring identity from holders.
type runnerInventoryReader interface {
	Team() store.Team
	ReadRunners(context.Context) (capability.RunnerSnapshot, error)
}

var _ runnerInventoryReader = (*store.Tenant)(nil)

func (s *Server) runnerInventory(ctx context.Context, team string) (capability.RunnerSnapshot, error) {
	reader, err := tenantPort[runnerInventoryReader](ctx, s, store.Team(team))
	if err != nil {
		return capability.RunnerSnapshot{}, err
	}
	snapshot, err := reader.ReadRunners(ctx)
	if err != nil {
		return capability.RunnerSnapshot{}, engineLookupError(err)
	}
	if snapshot.Now.IsZero() {
		return capability.RunnerSnapshot{}, routepolicy.MissingEndpointPolicy()
	}
	for _, row := range snapshot.Runners {
		if row.ID == "" || row.Team != team || row.Runner.Name == "" || row.Runner.Type == "" || row.MaxConcurrent < 0 || row.LastSeen.After(snapshot.Now) {
			return capability.RunnerSnapshot{}, routepolicy.MissingEndpointPolicy()
		}
		if h := row.Headroom; h != nil && (math.IsNaN(h.Cores) || math.IsInf(h.Cores, 0) || h.Cores < 0 || h.MemoryBytes < 0 || h.QueueDepth < 0 || h.ObservedAt.IsZero() || h.ObservedAt.After(snapshot.Now)) {
			return capability.RunnerSnapshot{}, routepolicy.MissingEndpointPolicy()
		}
	}
	return snapshot, nil
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	snapshot, err := s.runnerInventory(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	rows, page, err := pageCollection(r, a, snapshot.Runners, func(row capability.RunnerObservation) string { return row.ID })
	if err != nil {
		return err
	}
	out := make([]controllerwire.RunnerDetail, 0, len(rows))
	for _, row := range rows {
		active := append([]string{}, row.ActiveJobs...)
		slices.Sort(active)
		agent := controllerwire.RunnerDetail{
			Name: row.Runner.Name, Class: row.Runner.Type, Location: row.Location,
			Labels: append([]string{}, row.Runner.Labels...), ReservedPools: []string{}, Repos: []string{}, ActiveJobs: []controllerwire.RunnerJob{},
			Status: "idle", MaxConcurrent: row.MaxConcurrent,
		}
		for _, job := range slices.Compact(active) {
			run, node, _ := strings.Cut(job, "/")
			agent.ActiveJobs = append(agent.ActiveJobs, controllerwire.RunnerJob{RunID: run, JobID: node})
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
			agent.Headroom = &controllerwire.RunnerHeadroom{RunnerResources: controllerwire.RunnerResources{Cores: h.Cores, MemoryBytes: h.MemoryBytes}, QueueDepth: h.QueueDepth, ObservedAt: h.ObservedAt.UTC().Format(time.RFC3339Nano)}
		}
		out = append(out, agent)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"runners": out, "next_cursor": page.NextCursor, "truncated": page.Truncated})
	return nil
}

func (s *Server) sampleRunners(ctx context.Context) {
	var labels [][]string
	attempted, sampled := 0, 0
	err := s.operator.EachTeam(ctx, nil, func(tenant *store.Tenant) error {
		attempted++
		snapshot, err := tenant.ReadRunners(ctx)
		if err != nil {
			return err
		}
		sampled++
		for _, row := range snapshot.Runners {
			if !row.LastSeen.IsZero() && snapshot.Now.Sub(row.LastSeen) <= runnerLivenessWindow {
				labels = append(labels, row.Runner.Labels)
			}
		}
		return nil
	})
	if err != nil {
		s.logger.Error("runner sample failed", "err", err)
		return
	}
	if attempted > 0 && sampled == 0 {
		return
	}
	setActiveRunners(len(labels))
	liveRunners.sample(labels)
}
