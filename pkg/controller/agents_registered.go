package controller

import (
	"context"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) registeredAgents(ctx context.Context, now time.Time) ([]Agent, error) {
	executors, err := s.store.ListExecutors(ctx)
	if err != nil {
		return nil, err
	}
	active, err := s.store.ActiveExecutorActivity(ctx, now)
	if err != nil {
		return nil, err
	}
	out := make([]Agent, 0, len(executors))
	for _, e := range executors {
		activity := active[e.Name]
		jobs := activity.RunIDs
		status := "idle"
		lastSeen := ""
		if e.LastSeen.IsZero() || now.Sub(e.LastSeen) > store.ExecutorRegistrationActiveWindow {
			status = "offline"
		} else if len(jobs) > 0 {
			status = "busy"
		}
		if !e.LastSeen.IsZero() {
			lastSeen = e.LastSeen.UTC().Format(time.RFC3339)
		}
		labels := make(map[string]string, len(e.Capabilities))
		for _, capability := range e.Capabilities {
			key, value, found := strings.Cut(capability, "=")
			if !found {
				value = "true"
			}
			labels[key] = value
		}
		a := Agent{Name: e.Name, Type: e.Kind, Location: e.Location, Labels: labels, Capabilities: e.Capabilities, LastSeen: lastSeen, Status: status, ActiveJobs: jobs, ActiveSlots: &activity.ActiveSlots, MaxConcurrent: e.MaxConcurrent, BasePriority: e.BasePriority, PriorityCeiling: e.PriorityCeiling, Budget: AgentResources{Cores: e.Budget.Cores, MemoryBytes: e.Budget.MemoryBytes}}
		if e.HeadroomReported && status != "offline" {
			a.Headroom = &AgentHeadroom{
				Cores: e.Headroom.Cores, MemoryBytes: e.Headroom.MemoryBytes, QueueDepth: e.QueueDepth,
				ObservedAt: e.LastSeen.UTC().Format(time.RFC3339),
			}
		}
		out = append(out, a)
	}
	return out, nil
}
